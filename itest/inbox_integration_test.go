//go:build integration

package itest

import (
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/sectest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// inbox: my messages only, the unread ones and their count; marking one read keeps its first moment; another one's
// message answers 404; marking all read touches no one else's; no grant, no messages (NT08).
func (fx *fixture) inbox() {
	t := fx.t
	a := intent("test/buy", "due_reminder", principal(carol))
	b := intent("test/buy", "due_reminder", principal(carol), principal(sectest.Bob))
	for _, in := range []map[string]any{a, b} {
		fx.send("test/buy", in)
		fx.ended("test/buy", in)
	}
	mine := func(c client, q string) map[string]any {
		code, page := c.do("GET", "/api/notification/messages"+q, nil)
		require.Equal(t, 200, code, page)
		return page
	}
	id := func(c client, in map[string]any) string {
		for _, x := range mine(c, "?limit=200")["messages"].([]any) {
			m := x.(map[string]any)
			if fx.messageIntent(m["id"].(string)) == in["intent_id"] {
				return m["id"].(string)
			}
		}
		t.Fatalf("no message of %v", in["intent_id"])
		return ""
	}
	carolA, bobB := id(fx.carol, a), id(fx.bob, b)
	for _, x := range mine(fx.carol, "?limit=200")["messages"].([]any) {
		assert.NotEqual(t, bobB, x.(map[string]any)["id"], "only carol's own")
	}
	unread := mine(fx.carol, "")["unread"].(float64)
	code, m := fx.carol.do("POST", "/api/notification/messages/"+carolA+"/read", nil)
	require.Equal(t, 200, code, m)
	first := m["read_at"]
	assert.NotNil(t, first)
	_, m = fx.carol.do("POST", "/api/notification/messages/"+carolA+"/read", nil)
	assert.Equal(t, first, m["read_at"], "the first moment stays")
	assert.Equal(t, unread-1, mine(fx.carol, "")["unread"].(float64))
	for _, x := range mine(fx.carol, "?unread=true&limit=200")["messages"].([]any) {
		assert.NotEqual(t, carolA, x.(map[string]any)["id"], "a read message is not unread")
	}
	code, _ = fx.carol.do("POST", "/api/notification/messages/"+bobB+"/read", nil)
	assert.Equal(t, 404, code, "bob's message")
	code, _ = fx.carol.do("POST", "/api/notification/messages/"+ids.New().String()+"/read", nil)
	assert.Equal(t, 404, code)
	code, all := fx.carol.do("POST", "/api/notification/messages/read-all", nil)
	require.Equal(t, 200, code, all)
	assert.Zero(t, mine(fx.carol, "")["unread"])
	assert.NotZero(t, mine(fx.bob, "")["unread"], "bob's are not marked")
	code, _ = fx.dave.do("GET", "/api/notification/messages", nil)
	assert.Contains(t, []int{403, 404}, code, "no grant")
	code, _ = fx.carol.do("GET", "/api/notification/messages?unread=maybe", nil)
	assert.Equal(t, 400, code)
}
