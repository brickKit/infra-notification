// Package delivery delivers the pending intents (docs/design.md NT05-NT07, NT10, NT12): the positions expanded to
// their holders, each principal routed by the category and its preference, the in-app message written once, the
// result recorded and published; the expiry, its count and its alert; the purge of what is past its keeping.
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	core "github.com/brickKit/infra-notification/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/events"
	"github.com/brickKit/be-sdk-go/v3/flow"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/notify"
	"github.com/brickKit/be-sdk-go/v3/work"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
)

// The jobs (protocol 01): the delivery every minute (the floor; a new intent queues one at once) and the purge.
const (
	JobDeliver = "deliver"
	JobPurge   = "purge"
)

// Counters are the component's own metrics (NT12): the deliveries that expired, by category.
type Counters struct {
	Expired *prometheus.CounterVec
}

// NewCounters registers the counters on the member's registry (nil: none, the contract tests).
func NewCounters(r prometheus.Registerer) *Counters {
	c := &Counters{Expired: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "notification_deliveries_expired_total",
		Help: "Deliveries dropped because their intent expired before they could be made (NT12)."}, []string{"category"})}
	if r != nil {
		_ = r.Register(c.Expired)
	}
	return c
}

// pending is one claimed intent.
type pending struct {
	Sender, ID, Category, Title, Link string
	Template, CollapseKey             *string
	Params                            []byte
	Recipients                        []notify.Recipient
	ExpiresAt                         time.Time
}

// Deliver works the pending intents off, each in its own transaction (one that fails does not undo the others, and
// no lock is held across a batch), until every one has been tried once in this run: an intent still waiting for the
// employee projection is not claimed again until the next run, so it does not hold the ones behind it.
func Deliver(m *core.Base, c *Counters, ctx context.Context, _ *work.Run) error {
	tried := []string{}
	for {
		var n int
		err := m.RT.DB.BackgroundTx(ctx, func(ctx context.Context, tx db.Tx) error {
			one, err := claim(ctx, tx, tried)
			if err != nil {
				return err
			}
			n = len(one)
			for _, p := range one {
				tried = append(tried, p.Sender+"/"+p.ID)
				if _, err := deliver(m, c, ctx, tx, p); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil || n == 0 {
			return err
		}
	}
}

// claim locks the next pending intent not tried in this run, skipping the ones another replica works (protocol 01:
// an atomic claim).
func claim(ctx context.Context, tx db.Tx, tried []string) ([]pending, error) {
	rows, err := tx.Query(ctx, `SELECT sender, id::text, category, title, link, template, params, collapse_key,
			recipients, expires_at
		FROM intent WHERE state = 'pending' AND NOT (sender || '/' || id::text) = ANY($1::text[])
		ORDER BY received_at, sender, id LIMIT 1 FOR UPDATE SKIP LOCKED`, tried)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pending
	for rows.Next() {
		var p pending
		var recipients []byte
		if err := rows.Scan(&p.Sender, &p.ID, &p.Category, &p.Title, &p.Link, &p.Template, &p.Params, &p.CollapseKey,
			&recipients, &p.ExpiresAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(recipients, &p.Recipients); err != nil {
			return nil, fmt.Errorf("intent %s/%s: recipients: %w", p.Sender, p.ID, err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// deliver makes what can be made of one intent (NT05): done says it ended (every recipient has its result).
func deliver(m *core.Base, c *Counters, ctx context.Context, tx db.Tx, p pending) (bool, error) {
	now := time.Now()
	expired := !now.Before(p.ExpiresAt)
	var principals []string
	seen := map[string]bool{}
	waiting := false
	for _, r := range p.Recipients {
		if r.Principal != "" {
			if !seen[r.Principal] {
				seen[r.Principal] = true
				principals = append(principals, r.Principal)
			}
			continue
		}
		key := positionKey(*r.Position, now)
		var had bool // expanded once (or settled) already: its holders of that moment have their deliveries
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM delivery WHERE sender = $1 AND intent_id = $2::uuid
			AND recipient = $3)`, p.Sender, p.ID, key).Scan(&had); err != nil {
			return false, err
		}
		if had {
			continue
		}
		holders, result, err := expand(m, ctx, tx, *r.Position, now)
		if err != nil {
			return false, err
		}
		switch {
		case result == "" && expired: // still waiting for the projection when the intent expired
			result = core.Expired
		case result == "":
			waiting = true
			continue
		}
		if result == core.Written { // the holders join the principals; the position is recorded as expanded
			result = core.Expanded
			for _, h := range holders {
				if !seen[h] {
					seen[h] = true
					principals = append(principals, h)
				}
			}
		}
		if err := record(ctx, tx, p, key, core.InApp, result); err != nil {
			return false, err
		}
	}
	cat, _ := core.CategoryOf(p.Category)
	for _, who := range principals {
		for _, ch := range cat.Channels {
			if err := deliverTo(ctx, tx, p, who, ch, expired); err != nil {
				return false, err
			}
		}
	}
	if waiting {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE intent SET state = 'done', ended_at = now() WHERE sender = $1 AND id = $2::uuid`,
		p.Sender, p.ID); err != nil {
		return false, err
	}
	return true, publish(m, c, ctx, tx, p)
}

// expand is a position's holders on its date (NT06): result Written with the holders, Unresolvable, or "" while the
// projection is incomplete.
func expand(m *core.Base, ctx context.Context, tx db.Tx, pos notify.Position, now time.Time) ([]string, string, error) {
	if m.Directory == nil { // the deployment has no mdm/employee: nothing to wait for
		return nil, core.Unresolvable, nil
	}
	date := pos.Date
	if date == "" {
		date = now.UTC().Format(time.DateOnly)
	}
	holders, complete, err := m.Directory.Holders(ctx, tx, pos.LegalEntity, pos.PositionID, date)
	switch {
	case err != nil:
		return nil, "", err
	case !complete:
		return nil, "", nil
	case len(holders) == 0:
		return nil, core.Unresolvable, nil
	}
	return holders, core.Written, nil
}

// positionKey names a position recipient in the deliveries: the legal entity, the position and the date it is
// expanded on (two recipients of the same position on two dates are two).
func positionKey(p notify.Position, now time.Time) string {
	date := p.Date
	if date == "" {
		date = now.UTC().Format(time.DateOnly)
	}
	return "position:" + p.LegalEntity + "/" + p.PositionID + "@" + date
}

// deliverTo makes one principal's delivery on one channel once (P18.9): a result already recorded is not made again.
func deliverTo(ctx context.Context, tx db.Tx, p pending, who, channel string, expired bool) error {
	var done bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM delivery WHERE sender = $1 AND intent_id = $2::uuid
		AND recipient = $3 AND channel = $4)`, p.Sender, p.ID, who, channel).Scan(&done); err != nil || done {
		return err
	}
	var pref *bool
	if err := tx.QueryRow(ctx, `SELECT enabled FROM preference WHERE principal = $1 AND category = $2 AND channel = $3`,
		who, p.Category, channel).Scan(&pref); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	result := Route(p.Category, prefOf(pref), expired)
	if result == core.Written {
		if _, err := tx.Exec(ctx, `INSERT INTO message (id, principal, sender, intent_id, category, title, link, template,
				params, collapse_key) VALUES ($1, $2, $3, $4::uuid, $5, $6, $7, $8, $9, $10)`, ids.New().String(), who,
			p.Sender, p.ID, p.Category, p.Title, p.Link, p.Template, p.Params, p.CollapseKey); err != nil {
			return err
		}
	}
	return record(ctx, tx, p, who, channel, result)
}

func prefOf(p *bool) string {
	switch {
	case p == nil:
		return ""
	case *p:
		return "on"
	}
	return "off"
}

func record(ctx context.Context, tx db.Tx, p pending, recipient, channel, result string) error {
	_, err := tx.Exec(ctx, `INSERT INTO delivery (sender, intent_id, recipient, channel, result)
		VALUES ($1, $2::uuid, $3, $4, $5) ON CONFLICT DO NOTHING`, p.Sender, p.ID, recipient, channel, result)
	return err
}

// Result is one recipient's result on one channel in the delivery event (NT10).
type Result struct {
	Recipient string `json:"recipient"`
	Channel   string `json:"channel"`
	Result    string `json:"result"`
}

// publish emits the intent's delivery results (NT10), and counts and alerts what expired or could not be expanded
// (NT12).
func publish(m *core.Base, c *Counters, ctx context.Context, tx db.Tx, p pending) error {
	rows, err := tx.Query(ctx, `SELECT recipient, channel, result FROM delivery WHERE sender = $1 AND intent_id = $2::uuid
		ORDER BY recipient, channel`, p.Sender, p.ID)
	if err != nil {
		return err
	}
	results := []Result{}
	for rows.Next() {
		var r Result
		if err := rows.Scan(&r.Recipient, &r.Channel, &r.Result); err != nil {
			rows.Close()
			return err
		}
		results = append(results, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	cat, _ := core.CategoryOf(p.Category)
	for _, r := range results {
		if r.Result == core.Expired && c != nil {
			c.Expired.WithLabelValues(p.Category).Inc()
		}
		if cat.Mandatory && (r.Result == core.Expired || r.Result == core.Unresolvable) {
			m.RT.Logger.Error("a notification that must be delivered was not", "event", "notification_mandatory_undelivered",
				"sender", p.Sender, "intent", p.ID, "category", p.Category, "recipient", r.Recipient, "result", r.Result)
		}
	}
	fact := ids.New().String()
	_, err = m.RT.Events.Emit(ctx, tx, core.Stream, events.Event{Type: core.DeliveryRecorded, RecordID: fact, Version: 1,
		Projection: events.Full, Data: map[string]any{"fact_id": fact, "sender": p.Sender, "intent_id": p.ID,
			"category": p.Category, "results": results, "at": time.Now().UTC().Format(time.RFC3339Nano)}})
	return err
}

// Purge deletes what is past its keeping (NT11): the ended intents (and their deliveries), the messages, the merge
// keys' records.
func Purge(m *core.Base, ctx context.Context, _ *work.Run) error {
	return m.RT.DB.BackgroundTx(ctx, func(ctx context.Context, tx db.Tx) error {
		for _, q := range []struct {
			sql  string
			keep time.Duration
		}{
			{`DELETE FROM intent WHERE state <> 'pending' AND ended_at < now() - $1::interval`, core.IntentKept},
			{`DELETE FROM message WHERE created_at < now() - $1::interval`, core.MessageKept},
			{`DELETE FROM collapse WHERE at < now() - $1::interval`, core.CollapseKept},
			{`DELETE FROM rejected_intent WHERE at < now() - $1::interval`, core.RejectedKept},
		} {
			if _, err := tx.Exec(ctx, q.sql, q.keep.String()); err != nil {
				return err
			}
		}
		return nil
	})
}

// EmployeesChanged runs in the transaction that changed the employee projection (NT06): while intents wait for it, a
// delivery is queued at once (unless one is already queued), so a position is expanded without waiting for the
// minute's floor.
func EmployeesChanged(job string) func(ctx context.Context, tx db.Tx, w flow.Enqueuer) error {
	return func(ctx context.Context, tx db.Tx, w flow.Enqueuer) error {
		var due bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM intent WHERE state = 'pending')
			AND NOT EXISTS (SELECT 1 FROM be_work_item WHERE job = $1 AND state = 'pending')`, job).Scan(&due); err != nil || !due {
			return err
		}
		_, err := w.Enqueue(ctx, tx, job, struct{}{})
		return err
	}
}
