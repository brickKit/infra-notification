//go:build integration

package itest

import (
	"time"

	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/sectest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// delivery: an intent to two principals is written as one in-app message each (NT05); the same principal named twice
// gets one; the same intent arriving again is not delivered again (P18.9); the delivery results are recorded.
func (fx *fixture) delivery() {
	t := fx.t
	in := intent("test/buy", "approval_task", principal(sectest.Bob), principal(carol), principal(sectest.Bob))
	fx.send("test/buy", in)
	assert.Equal(t, "done", fx.ended("test/buy", in))
	assert.Equal(t, []bool{false}, fx.messagesOf(fx.bob, in), "one message, though bob is named twice")
	assert.Equal(t, []bool{false}, fx.messagesOf(fx.carol, in))
	assert.Equal(t, map[string]string{sectest.Bob: "written", carol: "written"}, fx.results("test/buy", in))
	code, page := fx.bob.do("GET", "/api/notification/messages", nil)
	require.Equal(t, 200, code)
	assert.EqualValues(t, 1, page["unread"])

	fx.processed(fx.send("test/buy", in)) // the same intent again (an at-least-once redelivery): taken, not failed
	assert.Equal(t, []bool{false}, fx.messagesOf(fx.carol, in), "not delivered twice")
	var events int
	require.NoError(t, fx.admin.QueryRow(fx.ctx, "SELECT count(*) FROM "+itSchema+".be_outbox WHERE type = "+
		"'infra.notification_delivery.recorded.v1' AND convert_from(payload, 'UTF8')::jsonb->>'intent_id' = $1", in["intent_id"]).Scan(&events))
	assert.Equal(t, 1, events, "one delivery result event per intent (NT10)")
}

// merging: a newer sequence of a merge key withdraws the earlier messages and delivers its own; a withdraw intent
// only withdraws; an older sequence arriving late is dropped; a withdraw that arrives before its intent leaves no
// message behind (NT04, P18.7).
func (fx *fixture) merging() {
	t := fx.t
	key := "task:" + ids.New().String()
	first := intent("test/buy", "approval_task", principal(sectest.Bob))
	first["collapse_key"], first["collapse_seq"] = key, 1
	fx.send("test/buy", first)
	fx.ended("test/buy", first)
	second := intent("test/buy", "approval_task", principal(sectest.Bob))
	second["collapse_key"], second["collapse_seq"] = key, 2
	fx.send("test/buy", second)
	fx.ended("test/buy", second)
	assert.Equal(t, []bool{true}, fx.messagesOf(fx.bob, first), "withdrawn by the newer one")
	assert.Equal(t, []bool{false}, fx.messagesOf(fx.bob, second))
	w := withdraw("test/buy", key, 3)
	fx.send("test/buy", w)
	assert.Equal(t, "done", fx.ended("test/buy", w))
	assert.Equal(t, []bool{true}, fx.messagesOf(fx.bob, second), "the withdraw intent withdraws it")
	late := intent("test/buy", "approval_task", principal(sectest.Bob))
	late["collapse_key"], late["collapse_seq"] = key, 2
	fx.send("test/buy", late)
	assert.Equal(t, "superseded", fx.ended("test/buy", late), "an older sequence arriving late is dropped")
	assert.Empty(t, fx.messagesOf(fx.bob, late))

	// out of order: the task was decided (sequence 5) before its delivery (sequence 4) arrived
	key2 := "task:" + ids.New().String()
	fx.send("test/buy", withdraw("test/buy", key2, 5))
	early := intent("test/buy", "approval_task", principal(sectest.Bob))
	early["collapse_key"], early["collapse_seq"] = key2, 4
	time.Sleep(200 * time.Millisecond)
	fx.send("test/buy", early)
	assert.Equal(t, "superseded", fx.ended("test/buy", early))
	assert.Empty(t, fx.messagesOf(fx.bob, early), "no message the withdraw could no longer reach")
}

// expiry: an intent past its expiry is not delivered any further; the recipients still without a result are recorded
// expired, and the intent ends at the next delivery run (NT05, P18.6).
func (fx *fixture) expiry() {
	t := fx.t
	pos := ids.New().String()
	fx.dir.set(fx.le+"/"+pos, false) // the projection is incomplete: the position waits
	in := intent("test/pay", "due_reminder", principal(carol), fx.position(pos))
	in["expires_at"] = time.Now().UTC().Add(3 * time.Second).Format(time.RFC3339Nano)
	fx.send("test/pay", in)
	fx.taken("test/pay", in)
	assert.Equal(t, []bool{false}, fx.eventually(fx.carol, in), "the principal is delivered at once")
	var state string
	require.NoError(t, fx.admin.QueryRow(fx.ctx, "SELECT state FROM "+itSchema+".intent WHERE id = $1", in["intent_id"]).Scan(&state))
	assert.Equal(t, "pending", state, "the position still waits")
	exp, err := time.Parse(time.RFC3339Nano, in["expires_at"].(string))
	require.NoError(t, err)
	time.Sleep(time.Until(exp) + 100*time.Millisecond)
	fx.send("test/pay", intent("test/pay", "due_reminder", principal(carol))) // the next delivery run (or the minute's floor)
	assert.Equal(t, "done", fx.ended("test/pay", in), "it ends when it has expired")
	assert.Equal(t, map[string]string{carol: "written", "position:" + fx.le + "/" + pos: "expired"}, fx.results("test/pay", in))
	fx.dir.set(fx.le+"/"+pos, true)
}

// eventually waits until a reader has a message of the intent.
func (fx *fixture) eventually(c client, in map[string]any) []bool {
	fx.t.Helper()
	var got []bool
	require.Eventually(fx.t, func() bool { got = fx.messagesOf(c, in); return len(got) > 0 }, 20*time.Second,
		100*time.Millisecond)
	return got
}
