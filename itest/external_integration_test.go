//go:build integration

package itest

import (
	"context"
	"sync"
	"time"

	"github.com/brickKit/be-sdk-go/v3/channelpb"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/sectest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeChannel answers be.channel.v1.ChannelService as a channel member would: accepted, unmapped for the principals
// it has no account for, unavailable while down; every call recorded.
type fakeChannel struct {
	channelpb.UnimplementedChannelServiceServer
	mu       sync.Mutex
	down     bool
	unmapped map[string]bool
	calls    []*channelpb.DeliverRequest
}

func (f *fakeChannel) Deliver(_ context.Context, r *channelpb.DeliverRequest) (*channelpb.DeliverResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r)
	switch {
	case f.down:
		return nil, status.Error(codes.Unavailable, "the channel is down")
	case f.unmapped[r.GetPrincipal()]:
		return &channelpb.DeliverResponse{Outcome: channelpb.Outcome_OUTCOME_UNMAPPED}, nil
	}
	return &channelpb.DeliverResponse{Outcome: channelpb.Outcome_OUTCOME_ACCEPTED}, nil
}

func (f *fakeChannel) setDown(d bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = d
}

// callsOf are the calls for an intent.
func (f *fakeChannel) callsOf(intent any) []*channelpb.DeliverRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*channelpb.DeliverRequest
	for _, c := range f.calls {
		if c.GetIntentId() == intent {
			out = append(out, c)
		}
	}
	return out
}

// external is an intent's result on the dingtalk channel for a recipient, waiting until it is not pending (or the
// want given).
func (fx *fixture) external(from string, in map[string]any, who, want string) string {
	fx.t.Helper()
	var got string
	require.Eventually(fx.t, func() bool {
		err := fx.admin.QueryRow(fx.ctx, "SELECT result FROM "+itSchema+".delivery WHERE sender = $1 AND intent_id = $2"+
			" AND recipient = $3 AND channel = 'dingtalk'", from, in["intent_id"], who).Scan(&got)
		return err == nil && got == want
	}, 30*time.Second, 100*time.Millisecond, "%v's dingtalk delivery to %s: want %s, last %s", in["intent_id"], who, want, got)
	return got
}

func (fx *fixture) resultEvents(in map[string]any) int {
	var n int
	require.NoError(fx.t, fx.admin.QueryRow(fx.ctx, "SELECT count(*) FROM "+itSchema+".be_outbox WHERE type = "+
		"'infra.notification_delivery.recorded.v1' AND convert_from(payload, 'UTF8')::jsonb->>'intent_id' = $1",
		in["intent_id"]).Scan(&n))
	return n
}

// externals: the external channel (NT13, NT14; P20.1, P20.3, P20.4): an approval goes in-app and to dingtalk; the
// dingtalk member gets the recipient's grant only; no account answers unmapped; a down member is retried with the
// same delivery id and the in-app message does not wait; the result event waits for the external deliveries; a
// category without external channels has none; the preference turns dingtalk off, even for a mandatory category
// whose in-app stays locked; a superseded or expired intent's pending deliveries are not handed over.
func (fx *fixture) externals() {
	t := fx.t
	grant := ids.New().String()
	in := intent("test/buy", "approval_task", principal(sectest.Bob), principal(carol))
	in["actions"] = map[string]any{sectest.Bob: map[string]any{"grant_id": grant, "actions": []any{
		map[string]any{"action": "approve", "label": "同意"}}}}
	in["action_channels"] = []any{"integration/im-dingtalk"}
	fx.send("test/buy", in)
	fx.ended("test/buy", in)
	assert.Equal(t, "handed_over", fx.external("test/buy", in, sectest.Bob, "handed_over"))
	assert.Equal(t, "unmapped", fx.external("test/buy", in, carol, "unmapped"))
	assert.Equal(t, map[string]string{sectest.Bob: "written", carol: "written"}, fx.results("test/buy", in),
		"in-app is written whatever the channel answers")
	calls := fx.chn.callsOf(in["intent_id"])
	require.Len(t, calls, 2)
	for _, c := range calls {
		assert.Equal(t, "test/buy", c.GetSender())
		assert.Equal(t, "approval_task", c.GetCategory())
		if c.GetPrincipal() == sectest.Bob {
			require.NotNil(t, c.GetAction(), "bob's grant goes to the channel that takes actions")
			assert.Equal(t, grant, c.GetAction().GetGrantId())
		} else {
			assert.Nil(t, c.GetAction(), "no grant for carol")
		}
	}
	require.Eventually(t, func() bool { return fx.resultEvents(in) == 1 }, 10*time.Second, 100*time.Millisecond)

	// a grant for a channel that does not take actions is not handed to dingtalk (P18.11, NT14)
	elsewhere := intent("test/buy", "approval_task", principal(sectest.Bob))
	elsewhere["actions"] = in["actions"]
	elsewhere["action_channels"] = []any{"integration/im-feishu"}
	fx.send("test/buy", elsewhere)
	fx.ended("test/buy", elsewhere)
	fx.external("test/buy", elsewhere, sectest.Bob, "handed_over")
	ec := fx.chn.callsOf(elsewhere["intent_id"])
	require.Len(t, ec, 1)
	assert.Nil(t, ec[0].GetAction(), "dingtalk does not take actions for this sender")

	// a down member: retried later with the same delivery id; the in-app message is written meanwhile and the result
	// event waits for the external delivery
	fx.chn.setDown(true)
	retry := intent("test/buy", "approval_task", principal(sectest.Bob))
	fx.send("test/buy", retry)
	fx.ended("test/buy", retry)
	assert.Equal(t, []bool{false}, fx.messagesOf(fx.bob, retry))
	require.Eventually(t, func() bool { return len(fx.chn.callsOf(retry["intent_id"])) >= 1 }, 20*time.Second, 100*time.Millisecond)
	assert.Equal(t, 0, fx.resultEvents(retry), "not published while an external delivery is pending (NT10)")
	fx.chn.setDown(false)
	_, err := fx.admin.Exec(fx.ctx, "UPDATE "+itSchema+".delivery SET next_at = now() WHERE intent_id = $1 AND result = 'pending'",
		retry["intent_id"]) // the backoff over
	require.NoError(t, err)
	fx.external("test/buy", retry, sectest.Bob, "handed_over")
	rc := fx.chn.callsOf(retry["intent_id"])
	require.GreaterOrEqual(t, len(rc), 2)
	assert.Equal(t, rc[0].GetDeliveryId(), rc[len(rc)-1].GetDeliveryId(), "the same delivery id on every try")
	require.Eventually(t, func() bool { return fx.resultEvents(retry) == 1 }, 20*time.Second, 100*time.Millisecond)

	// a category without external channels
	plain := intent("test/buy", "external_comm", principal(sectest.Bob))
	fx.send("test/buy", plain)
	fx.ended("test/buy", plain)
	var n int
	require.NoError(t, fx.admin.QueryRow(fx.ctx, "SELECT count(*) FROM "+itSchema+".delivery WHERE intent_id = $1 AND channel = 'dingtalk'",
		plain["intent_id"]).Scan(&n))
	assert.Zero(t, n)

	// the preference turns dingtalk off; a mandatory category's dingtalk can be turned off, its in-app cannot
	code, out := fx.bob.do("PUT", "/api/notification/preferences/approval_task/dingtalk", map[string]any{"enabled": false})
	require.Equal(t, 200, code, "%v", out)
	code, _ = fx.bob.do("PUT", "/api/notification/preferences/security_alert/dingtalk", map[string]any{"enabled": false})
	assert.Equal(t, 200, code)
	code, _ = fx.bob.do("PUT", "/api/notification/preferences/security_alert/in_app", map[string]any{"enabled": false})
	assert.Equal(t, 400, code)
	off := intent("test/buy", "security_alert", principal(sectest.Bob))
	fx.send("test/buy", off)
	fx.ended("test/buy", off)
	fx.external("test/buy", off, sectest.Bob, "preference_off")
	assert.Equal(t, map[string]string{sectest.Bob: "written"}, fx.results("test/buy", off))
	code, _ = fx.bob.do("PUT", "/api/notification/preferences/approval_task/dingtalk", map[string]any{"enabled": true})
	require.Equal(t, 200, code)

	// superseded and expired while pending: not handed over
	fx.chn.setDown(true)
	key := "task:" + ids.New().String()
	first := intent("test/buy", "approval_task", principal(sectest.Bob))
	first["collapse_key"], first["collapse_seq"] = key, 1
	fx.send("test/buy", first)
	fx.ended("test/buy", first)
	fx.external("test/buy", first, sectest.Bob, "pending")
	fx.processed(fx.send("test/buy", withdraw("test/buy", key, 2)))
	fx.external("test/buy", first, sectest.Bob, "superseded")
	short := intent("test/buy", "approval_task", principal(sectest.Bob))
	short["expires_at"] = time.Now().UTC().Add(3 * time.Second).Format(time.RFC3339Nano)
	fx.send("test/buy", short)
	fx.ended("test/buy", short)
	time.Sleep(3 * time.Second)
	_, err = fx.admin.Exec(fx.ctx, "UPDATE "+itSchema+".delivery SET next_at = now() WHERE intent_id = $1 AND result = 'pending'",
		short["intent_id"])
	require.NoError(t, err)
	fx.external("test/buy", short, sectest.Bob, "expired")
	fx.chn.setDown(false)
	before := len(fx.chn.callsOf(first["intent_id"]))
	time.Sleep(time.Second)
	assert.Equal(t, before, len(fx.chn.callsOf(first["intent_id"])), "a superseded delivery is not handed over")
	require.Eventually(t, func() bool { return fx.resultEvents(first) == 1 }, 30*time.Second, 200*time.Millisecond,
		"the superseded delivery settled the done intent: its result goes out")
}
