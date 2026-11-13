package delivery

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	core "github.com/brickKit/infra-notification/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/channelpb"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/errs"
	"github.com/brickKit/be-sdk-go/v3/notify"
	"github.com/brickKit/be-sdk-go/v3/work"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// JobExternal is the external deliveries' job (NT13), every 15 seconds; JobExternalNow is the same run queued in the
// transaction that wrote an intent's pending external deliveries (one queued at a time).
const (
	JobExternal    = "deliver-external"
	JobExternalNow = "deliver-external-now"
)

// queueExternal queues a run of the external job when the intent has pending external deliveries and none is queued.
func queueExternal(m *core.Base, ctx context.Context, tx db.Tx, p pending) error {
	if m.RT.Work == nil || len(m.Channels) == 0 {
		return nil
	}
	var due bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM delivery WHERE sender = $1 AND intent_id = $2::uuid
		AND result = 'pending') AND NOT EXISTS (SELECT 1 FROM be_work_item WHERE job = $3 AND state = 'pending')`,
		p.Sender, p.ID, JobExternalNow).Scan(&due); err != nil || !due {
		return err
	}
	_, err := m.RT.Work.Enqueue(ctx, tx, JobExternalNow, struct{}{})
	return err
}

// The external job's parameters (NT13; protocol 10's backoff).
const (
	ExternalBatch   = 200
	ExternalLease   = time.Minute // a claimed row is not claimed again meanwhile (the call is outside a transaction)
	ExternalCall    = 10 * time.Second
	BackoffStart    = time.Minute
	BackoffCap      = time.Hour
	UnreachableWarn = time.Hour
)

// external is one claimed external delivery and what the call needs.
type external struct {
	sender, intent, recipient, channel, deliveryID string
	category, title, link                          string
	collapseKey                                    *string
	collapseSeq                                    *int64
	expiresAt                                      time.Time
	attempts                                       int
	since                                          time.Time // the delivery row's creation
	action                                         *channelpb.Action
}

// DeliverExternal hands the due external deliveries to their channel members (NT13): claimed by row (a lease, so the
// call happens outside any transaction), the result recorded in a new transaction; a retryable failure backs off; a
// channel unreachable once is skipped for the rest of the run. An intent whose last external delivery settles
// publishes its delivery result (NT10).
func DeliverExternal(m *core.Base, c *Counters, ctx context.Context, _ *work.Run) error {
	var due []external
	err := m.RT.DB.BackgroundTx(ctx, func(ctx context.Context, tx db.Tx) error {
		rows, err := tx.Query(ctx, `SELECT d.sender, d.intent_id::text, d.recipient, d.channel, d.delivery_id::text,
				d.attempts, d.at, i.category, i.title, i.link, i.collapse_key, i.collapse_seq, i.expires_at, i.actions,
				i.action_channels
			FROM delivery d JOIN intent i ON i.sender = d.sender AND i.id = d.intent_id
			WHERE d.result = 'pending' AND d.next_at <= now()
			ORDER BY d.next_at LIMIT $1 FOR UPDATE OF d SKIP LOCKED`, ExternalBatch)
		if err != nil {
			return err
		}
		for rows.Next() {
			var e external
			var actions []byte
			var actionChannels []string
			if err := rows.Scan(&e.sender, &e.intent, &e.recipient, &e.channel, &e.deliveryID, &e.attempts, &e.since,
				&e.category, &e.title, &e.link, &e.collapseKey, &e.collapseSeq, &e.expiresAt, &actions, &actionChannels); err != nil {
				rows.Close()
				return err
			}
			e.action = actionFor(m, actions, actionChannels, e.channel, e.recipient)
			due = append(due, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, e := range due { // the lease: not claimed again while this run calls
			if _, err := tx.Exec(ctx, `UPDATE delivery SET next_at = now() + $5::interval WHERE sender = $1
				AND intent_id = $2::uuid AND recipient = $3 AND channel = $4`, e.sender, e.intent, e.recipient, e.channel,
				ExternalLease.String()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := sweep(m, c, ctx); err != nil {
		return err
	}
	down := map[string]bool{}
	for _, e := range due {
		result, next := core.Pending, time.Time{}
		switch {
		case !time.Now().Before(e.expiresAt):
			result = core.Expired
		case down[e.channel]:
			next = time.Now() // the channel did not answer this run: next run
		default:
			result, next = call(m, ctx, e)
			if result == core.Pending && next.IsZero() {
				down[e.channel] = true
				next = time.Now().Add(backoff(e.attempts + 1))
			}
		}
		if err := m.RT.DB.BackgroundTx(ctx, func(ctx context.Context, tx db.Tx) error {
			return settle(m, c, ctx, tx, e, result, next)
		}); err != nil {
			return err
		}
	}
	return nil
}

// actionFor is the recipient's action for a channel, when the intent carries one and the channel takes actions
// (P18.11, NT14).
func actionFor(m *core.Base, actions []byte, actionChannels []string, channel, recipient string) *channelpb.Action {
	if len(actions) == 0 {
		return nil
	}
	comp := ""
	for _, ch := range m.Channels {
		if ch.Name == channel {
			comp = ch.Component
		}
	}
	if !slices.Contains(actionChannels, comp) {
		return nil
	}
	var grants map[string]notify.Grant
	if json.Unmarshal(actions, &grants) != nil {
		return nil
	}
	g, ok := grants[recipient]
	if !ok {
		return nil
	}
	a := &channelpb.Action{GrantId: g.ID}
	for _, c := range g.Actions {
		a.Choices = append(a.Choices, &channelpb.ActionChoice{Action: c.Action, Label: c.Label})
	}
	return a
}

// call hands one delivery to its channel member (P20.1): the result, and when to try again for a retryable failure
// that is the member's own answer (zero: the member did not answer at all).
func call(m *core.Base, ctx context.Context, e external) (string, time.Time) {
	comp := ""
	for _, ch := range m.Channels {
		if ch.Name == e.channel {
			comp = ch.Component
		}
	}
	conn, ok := m.RT.GRPC(comp)
	if !ok {
		m.RT.Logger.Error("an external channel's member is not in the deployment", "event", "notification_channel_missing",
			"channel", e.channel, "component", comp)
		return core.Failed, time.Time{}
	}
	req := &channelpb.DeliverRequest{DeliveryId: e.deliveryID, Sender: e.sender, IntentId: e.intent, Principal: e.recipient,
		Category: e.category, Title: e.title, Link: e.link, ExpiresAt: e.expiresAt.UTC().Format(time.RFC3339Nano),
		Action: e.action}
	if e.collapseKey != nil && e.collapseSeq != nil {
		req.CollapseKey, req.CollapseSeq = *e.collapseKey, *e.collapseSeq
	}
	cctx, cancel := context.WithTimeout(ctx, ExternalCall)
	defer cancel()
	res, err := channelpb.NewChannelServiceClient(conn).Deliver(cctx, req)
	switch {
	case err == nil && res.GetOutcome() == channelpb.Outcome_OUTCOME_ACCEPTED:
		return core.HandedOver, time.Time{}
	case err == nil && res.GetOutcome() == channelpb.Outcome_OUTCOME_UNMAPPED:
		return core.Unmapped, time.Time{}
	case err == nil:
		return core.Failed, time.Time{}
	}
	if retryable(err) {
		if code := status.Code(err); code == codes.Unavailable || code == codes.DeadlineExceeded {
			return core.Pending, time.Time{} // unreachable: the channel is skipped for the rest of the run
		}
		return core.Pending, time.Now().Add(backoff(e.attempts + 1))
	}
	m.RT.Logger.Error("an external channel refused a delivery", "event", "notification_channel_refused",
		"channel", e.channel, "sender", e.sender, "intent", e.intent, "error", err.Error())
	return core.Failed, time.Time{}
}

// retryable is a channel member's answer that may change: unreachable, timed out, a definition or a view not ready
// (NT13); the rest (REQUEST_INVALID, IDEMPOTENCY_KEY_REUSED, PERMISSION_DENIED, ...) are final.
func retryable(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted, codes.Aborted:
		return true
	}
	if e, _, ok := errs.FromGRPC(err); ok {
		return e.Reason == errs.ReasonDefinitionPending || e.Reason == errs.ReasonAuthorizationViewExpired ||
			e.Reason == errs.ReasonDependencyUnavailable
	}
	return false
}

func backoff(attempts int) time.Duration {
	d := BackoffStart
	for i := 1; i < attempts && d < BackoffCap; i++ {
		d *= 2
	}
	return min(d, BackoffCap)
}

// settle records one external delivery's result, or when it is tried next; a long unreachable channel is alerted
// (NT12); the intent publishes its results once its last external delivery settled (NT10).
func settle(m *core.Base, c *Counters, ctx context.Context, tx db.Tx, e external, result string, next time.Time) error {
	if result == core.Pending {
		if time.Since(e.since) > UnreachableWarn && e.attempts > 0 {
			m.RT.Logger.Error("an external channel has not taken a delivery for an hour", "event",
				"notification_channel_unreachable", "channel", e.channel, "sender", e.sender, "intent", e.intent)
		}
		_, err := tx.Exec(ctx, `UPDATE delivery SET next_at = $5, attempts = attempts + 1 WHERE sender = $1
			AND intent_id = $2::uuid AND recipient = $3 AND channel = $4 AND result = 'pending'`, e.sender, e.intent,
			e.recipient, e.channel, next)
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE delivery SET result = $5, at = now(), next_at = NULL, attempts = attempts + 1
		WHERE sender = $1 AND intent_id = $2::uuid AND recipient = $3 AND channel = $4 AND result = 'pending'`,
		e.sender, e.intent, e.recipient, e.channel, result)
	if err != nil || tag.RowsAffected() == 0 { // superseded meanwhile: its result stands
		return err
	}
	return publishSettled(m, c, ctx, tx, e.sender, e.intent)
}

// sweep publishes the done intents whose external deliveries all settled outside settle (superseded by a later
// intent, NT04): their delivery results go out once (NT10).
func sweep(m *core.Base, c *Counters, ctx context.Context) error {
	return m.RT.DB.BackgroundTx(ctx, func(ctx context.Context, tx db.Tx) error {
		rows, err := tx.Query(ctx, `SELECT sender, id::text FROM intent i WHERE state = 'done' AND published_at IS NULL
			AND NOT EXISTS (SELECT 1 FROM delivery d WHERE d.sender = i.sender AND d.intent_id = i.id AND d.result = 'pending')
			LIMIT 100`)
		if err != nil {
			return err
		}
		var keys [][2]string
		for rows.Next() {
			var k [2]string
			if err := rows.Scan(&k[0], &k[1]); err != nil {
				rows.Close()
				return err
			}
			keys = append(keys, k)
		}
		rows.Close()
		for _, k := range keys {
			if err := publishSettled(m, c, ctx, tx, k[0], k[1]); err != nil {
				return err
			}
		}
		return rows.Err()
	})
}
