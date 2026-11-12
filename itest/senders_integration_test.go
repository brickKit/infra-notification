//go:build integration

package itest

import (
	"time"

	core "github.com/brickKit/infra-notification/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/events"
	"github.com/brickKit/be-sdk-go/v3/recoverypb"
	"github.com/brickKit/be-sdk-go/v3/sectest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

// senderCheck: an intent whose sender is not its envelope's source, one off the shared contract, one of a category
// outside the catalog and one that expires before it occurred are refused, listed for the managers and audited
// (NT02); a component the deployment did not list is not even subscribed to (NT01).
func (fx *fixture) senderCheck() {
	t := fx.t
	forged := intent("test/buy", "approval_task", principal(sectest.Bob)) // sent on test/pay's stream
	bad := intent("test/buy", "approval_task", principal(sectest.Bob))
	delete(bad, "title")
	unknown := intent("test/buy", "newsletter", principal(sectest.Bob))
	backwards := intent("test/buy", "approval_task", principal(sectest.Bob))
	backwards["expires_at"] = "2020-01-01T00:00:00Z"
	evil := intent("test/evil", "approval_task", principal(sectest.Bob))
	// test/pay on its own stream, writing test/buy as both the envelope's source and the payload's sender: the stream
	// is test/pay's, so it is test/pay that sent it (P18.8)
	disguised := intent("test/buy", "approval_task", principal(sectest.Bob))
	fx.publishAs("test/pay", "test/buy", "test.pay_notification.intent.v1", disguised["intent_id"].(string), 1, events.Full,
		disguised)
	fx.send("test/pay", forged)
	fx.send("test/pay", forged) // redelivered: recorded once
	fx.send("test/evil", evil)
	for _, in := range []map[string]any{bad, unknown, backwards} {
		fx.send("test/buy", in)
	}
	want := map[any]string{forged["intent_id"]: "not_the_sender", disguised["intent_id"]: "not_the_sender", bad["intent_id"]: "malformed",
		unknown["intent_id"]: "unknown_category", backwards["intent_id"]: "malformed"}
	got := map[any]string{}
	require.Eventually(t, func() bool {
		code, page := fx.alice.do("GET", "/api/notification/rejected-intents?limit=200", nil)
		if code != 200 {
			return false
		}
		for _, x := range page["intents"].([]any) {
			p := x.(map[string]any)
			got[p["intent_id"]] = p["reason"].(string)
		}
		return len(got) >= 5
	}, 10*time.Second, 100*time.Millisecond, "the refused intents are listed")
	var times int
	require.NoError(t, fx.admin.QueryRow(fx.ctx, "SELECT count(*) FROM "+itSchema+".rejected_intent WHERE intent_id = $1",
		forged["intent_id"]).Scan(&times))
	assert.Equal(t, 1, times, "a redelivered refusal is recorded once")
	for id, reason := range want {
		assert.Equal(t, reason, got[id], id)
	}
	assert.NotContains(t, got, evil["intent_id"], "test/evil is not subscribed to")
	var n int
	require.NoError(t, fx.admin.QueryRow(fx.ctx, "SELECT count(*) FROM "+itSchema+".intent WHERE id = ANY($1::uuid[])",
		[]any{forged["intent_id"], disguised["intent_id"], bad["intent_id"], unknown["intent_id"], backwards["intent_id"],
			evil["intent_id"]}).Scan(&n))
	assert.Zero(t, n, "a refused intent delivers nothing")
	code, out := fx.bob.do("GET", "/api/notification/rejected-intents", nil)
	assert.Contains(t, []int{403, 404}, code, "the managers only: %v", out)
	var audited int
	require.NoError(t, fx.admin.QueryRow(fx.ctx, "SELECT count(*) FROM "+itSchema+".be_audit_log WHERE category = 'security_denial'").
		Scan(&audited))
	assert.Equal(t, 5, audited, "each refusal has one security record (the redelivered one none more)")
}

// recoveryChecks: a full recovery's checks are the recovery tool's only (be-protocol chapter 14); the notification
// component audits no business object (its intents come again by redelivery), so the SDK's own checks answer.
func (fx *fixture) recoveryChecks() {
	t := fx.t
	dial := func(as string) *grpc.ClientConn {
		conn, err := grpc.NewClient(itSystem, grpc.WithTransportCredentials(credentials.NewTLS(fx.w.TLS(t, as).ClientConfig(core.ID))))
		require.NoError(t, err)
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	_, err := recoverypb.NewCheckServiceClient(dial("test/buy")).Check(fx.ctx, &recoverypb.CheckRequest{Recovery: "r"})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "the recovery tool only: %v", err)
	res, err := recoverypb.NewCheckServiceClient(dial("ops/recover")).Check(fx.ctx, &recoverypb.CheckRequest{Recovery: "r"})
	require.NoError(t, err)
	assert.NotEmpty(t, res.GetResults(), "the SDK's own checks run")
}
