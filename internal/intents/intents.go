// Package intents takes the notification intents of the deployment's senders in (docs/design.md NT01, NT02, NT04):
// the publisher check, the shape by the shared intent contract, the duplicate, the merge key's sequence and what a
// newer one supersedes; the refused intents (their purge is the delivery module's).
package intents

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	core "github.com/brickKit/infra-notification/v3/internal/core"
	sources "github.com/brickKit/infra-notification/v3/internal/sources"

	"github.com/brickKit/be-sdk-go/v3/audit"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/events"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/notify"
)

// JobDeliverNow is the delivery queued in the transaction that takes a deliver intent in (the delivery module runs
// it; the interval job is the floor).
const JobDeliverNow = "deliver-now"

// Subs are the subscriptions the configuration makes (NT01): each sender's stream, its intents; the handler is told
// whose stream it is.
func Subs(m *core.Base, handle func(*core.Base, string, context.Context, db.Tx, events.Received) error) []events.Sub {
	var out []events.Sub
	for _, c := range m.Senders {
		s, owner := sources.Stream(c), c
		out = append(out, events.Sub{Name: "intents-" + strings.ReplaceAll(s, ".", "-"), Streams: []string{s},
			Types: []string{sources.Type(c)},
			Handle: func(ctx context.Context, tx db.Tx, e events.Received) error {
				return handle(m, owner, ctx, tx, e)
			}})
	}
	return out
}

// OnEvent takes one intent of a subscribed sender in, in the consumer's transaction (NT02, NT04). owner is the
// component whose stream it came on: the bus lets only its service identity publish there, so it is the sender; the
// envelope's source and the payload's sender are what the publisher wrote, and must both name it (P18.8).
func OnEvent(m *core.Base, owner string, ctx context.Context, tx db.Tx, e events.Received) error {
	from := owner
	var in notify.Intent
	parsed := json.Unmarshal(e.Data, &in) == nil
	id := in.ID
	if id == "" {
		id = e.RecordID
	}
	a := Arrival{Kind: in.Kind, ShapeOK: parsed && shapeOK(e.Data), Category: in.Category,
		SenderIsSource:       in.Sender == owner && e.Source == "urn:be:"+owner && e.Stream == sources.Stream(owner),
		ExpiresAfterOccurred: notify.ExpiresInTime(in)}
	if a.SenderIsSource && a.ShapeOK {
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM intent WHERE sender = $1 AND id = $2::uuid)`, from, in.ID).
			Scan(&a.Seen); err != nil {
			return err
		}
		if in.CollapseKey != "" {
			seq := in.CollapseSeq
			a.CollapseSeq = &seq
			// the key's row is the lock of its intents: a first one is made (sequence 0: none seen) so that two first
			// intents of a key arriving together are judged one after the other
			if _, err := tx.Exec(ctx, `INSERT INTO collapse (sender, key, seq) VALUES ($1, $2, 0) ON CONFLICT DO NOTHING`,
				from, in.CollapseKey); err != nil {
				return err
			}
			var max int64
			if err := tx.QueryRow(ctx, `SELECT seq FROM collapse WHERE sender = $1 AND key = $2 FOR UPDATE`, from,
				in.CollapseKey).Scan(&max); err != nil {
				return err
			}
			if max > 0 {
				a.MaxSeq = &max
			}
		}
	}
	adm := Admit(a)
	switch adm.Answer {
	case Duplicate:
		return nil
	case Stale: // an older sequence of its merge key: kept only for the duplicate check (NT04)
		return insert(ctx, tx, in, "superseded")
	case Accepted:
	default:
		return reject(m, ctx, tx, from, id, adm.Answer)
	}
	if adm.Supersedes {
		if err := supersede(ctx, tx, in); err != nil {
			return err
		}
	}
	if !adm.Delivers { // a withdraw intent only supersedes
		return insert(ctx, tx, in, "done")
	}
	if err := insert(ctx, tx, in, "pending"); err != nil {
		return err
	}
	if m.RT.Work == nil {
		return nil
	}
	// one delivery run queued is enough: a backlog of intents does not queue a run each
	var queued bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM be_work_item WHERE job = $1 AND state = 'pending')`,
		JobDeliverNow).Scan(&queued); err != nil || queued {
		return err
	}
	_, err := m.RT.Work.Enqueue(ctx, tx, JobDeliverNow, struct{}{})
	return err
}

// shapeOK is the payload against the shared contract, its category aside: an unknown category is its own refusal,
// after the shape (P18.8's order).
func shapeOK(raw []byte) bool {
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return false
	}
	if _, ok := doc["category"].(string); ok {
		doc["category"] = core.Catalog[0].Name
	}
	b, _ := json.Marshal(doc)
	return notify.Validate(b) == nil
}

// supersede records the newer sequence of the merge key and withdraws what the earlier ones did: their pending
// intents are not delivered, their in-app messages are marked withdrawn (NT04).
func supersede(ctx context.Context, tx db.Tx, in notify.Intent) error {
	if _, err := tx.Exec(ctx, `UPDATE collapse SET seq = $3, at = now() WHERE sender = $1 AND key = $2`, in.Sender,
		in.CollapseKey, in.CollapseSeq); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE intent SET state = 'superseded', ended_at = now()
		WHERE sender = $1 AND collapse_key = $2 AND state = 'pending'`, in.Sender, in.CollapseKey); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE message SET withdrawn_at = now()
		WHERE sender = $1 AND collapse_key = $2 AND withdrawn_at IS NULL`, in.Sender, in.CollapseKey); err != nil {
		return err
	}
	// the external deliveries still waiting are not handed over any more (NT04, NT13)
	_, err := tx.Exec(ctx, `UPDATE delivery d SET result = 'superseded', at = now(), next_at = NULL FROM intent i
		WHERE d.result = 'pending' AND i.sender = d.sender AND i.id = d.intent_id AND i.sender = $1 AND i.collapse_key = $2
		AND i.collapse_seq < $3`, in.Sender, in.CollapseKey, in.CollapseSeq)
	return err
}

// insert keeps an intent taken in, in the state given.
func insert(ctx context.Context, tx db.Tx, in notify.Intent, state string) error {
	recipients, _ := json.Marshal(in.Recipients)
	if in.Recipients == nil {
		recipients = []byte("[]")
	}
	var params []byte
	if in.Params != nil {
		params, _ = json.Marshal(in.Params)
	}
	var actions []byte
	if len(in.Actions) > 0 { // the grants, not secrets; only handed to the channels that take actions (NT14)
		actions, _ = json.Marshal(in.Actions)
	}
	_, err := tx.Exec(ctx, `INSERT INTO intent (sender, id, category, kind, recipients, title, link, template, params,
			collapse_key, collapse_seq, occurred_at, expires_at, state, ended_at, actions, action_channels, in_app_only)
		VALUES ($1, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::timestamptz, $13::timestamptz, $14,
			CASE WHEN $14 = 'pending' THEN NULL ELSE now() END, $15, $16, $17)`,
		in.Sender, in.ID, in.Category, in.Kind, recipients, nullable(in.Title), nullable(in.Link), nullable(in.Template),
		params, nullable(in.CollapseKey), nullableSeq(in), in.OccurredAt, nullable(in.ExpiresAt), state, actions,
		in.ActionChannels, in.InAppOnly)
	return err
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullableSeq(in notify.Intent) *int64 {
	if in.CollapseKey == "" {
		return nil
	}
	return &in.CollapseSeq
}

// reject records a refused intent and its security record, delivering nothing (NT02); the same intent redelivered is
// recorded once.
func reject(m *core.Base, ctx context.Context, tx db.Tx, from, intentID, reason string) error {
	var seen bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM rejected_intent WHERE sender = $1 AND intent_id = $2
		AND reason = $3)`, from, intentID, reason).Scan(&seen); err != nil || seen {
		return err
	}
	m.RT.Logger.Warn("a notification intent was refused", "event", "notification_intent_refused", "sender", from,
		"intent", intentID, "reason", reason)
	if _, err := tx.Exec(ctx, `INSERT INTO rejected_intent (id, sender, intent_id, reason) VALUES ($1, $2, $3, $4)`,
		ids.New().String(), from, intentID, reason); err != nil {
		return err
	}
	if m.RT.Audit == nil {
		return nil
	}
	_, err := m.RT.Audit.Write(ctx, tx, audit.Record{Category: audit.SecurityDenial, Operation: core.OpRejectRead,
		Outcome: audit.Denied, Actor: from, Reason: reason, Target: audit.TargetOf(core.Rejections, intentID, nil)})
	return err
}

// Rejected lists the refused intents, the latest first (troubleshooting, NT02).
func Rejected(m *core.Base, c *httpx.Context) error {
	ctx, _, err := core.Check(c, core.OpRejectRead)
	if err != nil {
		return err
	}
	limit, err := core.Limit(c.Query("limit"))
	if err != nil {
		return err
	}
	after, err := core.FromCursor(c.Query("cursor"), 2)
	if err != nil {
		return err
	}
	type row struct {
		Sender   string `json:"sender"`
		IntentID string `json:"intent_id"`
		Reason   string `json:"reason"`
		At       string `json:"at"`
		id       string
	}
	out := []row{}
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		q := `SELECT id::text, sender, intent_id, reason, at FROM rejected_intent`
		args := []any{limit + 1}
		if after != nil {
			q += ` WHERE (at, id) < ($2::timestamptz, $3::uuid)`
			args = append(args, after[0], after[1])
		}
		rows, err := tx.Query(ctx, q+` ORDER BY at DESC, id DESC LIMIT $1`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r row
			var at time.Time
			if err := rows.Scan(&r.id, &r.Sender, &r.IntentID, &r.Reason, &at); err != nil {
				return err
			}
			r.At = at.UTC().Format(time.RFC3339Nano)
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		next = core.Cursor(last.At, last.id)
	}
	return c.JSON(200, map[string]any{"intents": out, "next_cursor": next})
}
