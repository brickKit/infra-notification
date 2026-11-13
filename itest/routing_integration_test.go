//go:build integration

package itest

import (
	"time"

	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/sectest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// preferences: the catalog with each one's default (NT03, NT09); a category turned off is not delivered and is
// recorded preference_off (NT07); one that must be delivered cannot be turned off and is delivered anyway; marketing
// is off by default.
func (fx *fixture) preferences() {
	t := fx.t
	code, prefs := fx.carol.do("GET", "/api/notification/preferences", nil)
	require.Equal(t, 200, code, prefs)
	byCat := map[string]map[string]any{}
	for _, x := range prefs["preferences"].([]any) {
		if p := x.(map[string]any); p["channel"] == "in_app" { // the in-app row of each category (externals: externals)
			byCat[p["category"].(string)] = p
		}
	}
	assert.Len(t, byCat, 6)
	assert.Equal(t, true, byCat["security_alert"]["mandatory"])
	assert.Equal(t, false, byCat["marketing"]["enabled"])

	code, out := fx.carol.do("PUT", "/api/notification/preferences/approval_task/in_app", map[string]any{"enabled": false})
	require.Equal(t, 200, code, out)
	for _, bad := range []string{"security_alert/in_app", "legal_notice/in_app", "newsletter/in_app", "approval_task/sms"} {
		code, out = fx.carol.do("PUT", "/api/notification/preferences/"+bad, map[string]any{"enabled": false})
		assert.Equal(t, 400, code, "%s: %v", bad, out)
	}
	off := intent("test/buy", "approval_task", principal(carol), principal(sectest.Bob))
	alert := intent("test/buy", "security_alert", principal(carol))
	promo := intent("test/buy", "marketing", principal(carol))
	for _, in := range []map[string]any{off, alert, promo} {
		fx.send("test/buy", in)
		fx.ended("test/buy", in)
	}
	assert.Equal(t, map[string]string{carol: "preference_off", sectest.Bob: "written"}, fx.results("test/buy", off))
	assert.Empty(t, fx.messagesOf(fx.carol, off))
	assert.Equal(t, []bool{false}, fx.messagesOf(fx.carol, alert), "must be delivered")
	// a row turning it off, left from before the category had to be delivered, changes nothing (NT07)
	_, err := fx.admin.Exec(fx.ctx, "INSERT INTO "+itSchema+".preference (principal, category, channel, enabled) "+
		"VALUES ($1, 'security_alert', 'in_app', false)", carol)
	require.NoError(t, err)
	alert2 := intent("test/buy", "security_alert", principal(carol))
	fx.send("test/buy", alert2)
	fx.ended("test/buy", alert2)
	assert.Equal(t, map[string]string{carol: "written"}, fx.results("test/buy", alert2))
	assert.Equal(t, map[string]string{carol: "preference_off"}, fx.results("test/buy", promo), "marketing is off by default")
	code, out = fx.carol.do("PUT", "/api/notification/preferences/approval_task/in_app", map[string]any{"enabled": true})
	require.Equal(t, 200, code, out)
}

// positions: a position expands to its holders when the delivery is made (NT06); the same person by name and by
// position gets one message; an incomplete projection makes it wait, not use what it has; a position nobody holds is
// unresolvable.
func (fx *fixture) positions() {
	t := fx.t
	pos, empty, later := ids.New().String(), ids.New().String(), ids.New().String()
	fx.dir.set(fx.le+"/"+pos, true, carol, sectest.Bob)
	fx.dir.set(fx.le+"/"+empty, true)
	in := intent("test/pay", "approval_task", fx.position(pos), principal(carol), fx.position(empty))
	fx.send("test/pay", in)
	assert.Equal(t, "done", fx.ended("test/pay", in))
	assert.Equal(t, map[string]string{carol: "written", sectest.Bob: "written", fx.posKey(pos): "expanded",
		fx.posKey(empty): "unresolvable"}, fx.results("test/pay", in))
	assert.Equal(t, []bool{false}, fx.messagesOf(fx.carol, in), "one message by name and by position")

	fx.dir.set(fx.le+"/"+later, false, sectest.Bob) // incomplete: bob is not to be relied on yet
	wait := intent("test/pay", "approval_task", fx.position(later))
	fx.send("test/pay", wait)
	fx.taken("test/pay", wait)
	assert.Empty(t, fx.messagesOf(fx.bob, wait), "not delivered by an incomplete projection")
	fx.dir.set(fx.le+"/"+later, true, carol)                      // complete now: carol holds it
	nudge := intent("test/pay", "due_reminder", principal(carol)) // a new intent queues a delivery run at once
	fx.send("test/pay", nudge)
	assert.Equal(t, "done", fx.ended("test/pay", wait))
	assert.Equal(t, map[string]string{carol: "written", fx.posKey(later): "expanded"}, fx.results("test/pay", wait))

	// a position waiting for the projection while the intent waits: a newer sequence of its merge key supersedes it
	// before it is ever delivered, and only the newer one is (NT04)
	queued := ids.New().String()
	fx.dir.set(fx.le+"/"+queued, false, carol)
	key := "task:" + ids.New().String()
	old := intent("test/pay", "approval_task", fx.position(queued))
	old["collapse_key"], old["collapse_seq"] = key, 1
	fx.send("test/pay", old)
	fx.taken("test/pay", old)
	newer := intent("test/pay", "approval_task", principal(carol))
	newer["collapse_key"], newer["collapse_seq"] = key, 2
	fx.send("test/pay", newer)
	assert.Equal(t, "superseded", fx.ended("test/pay", old), "never delivered")
	assert.Equal(t, "done", fx.ended("test/pay", newer))
	assert.Empty(t, fx.results("test/pay", old))
	assert.Equal(t, []bool{false}, fx.messagesOf(fx.carol, newer))
	fx.dir.set(fx.le+"/"+queued, true, carol)

	// a position expanded while another one of the same intent waits is not expanded again: its holder of that
	// moment has the message, the one who takes the position over later does not (NT06)
	first, second := ids.New().String(), ids.New().String()
	fx.dir.set(fx.le+"/"+first, true, carol)
	both := intent("test/pay", "due_reminder", fx.position(first), fx.position(second))
	fx.dir.set(fx.le+"/"+second, false)
	fx.send("test/pay", both)
	require.Eventually(t, func() bool { return fx.results("test/pay", both)[carol] == "written" }, 20*time.Second,
		100*time.Millisecond)
	fx.dir.set(fx.le+"/"+first, true, sectest.Bob)                            // bob holds it now
	fx.dir.set(fx.le+"/"+second, true)                                        // nobody holds the other
	fx.send("test/pay", intent("test/pay", "due_reminder", principal(carol))) // the next delivery run
	assert.Equal(t, "done", fx.ended("test/pay", both))
	assert.Equal(t, map[string]string{carol: "written", fx.posKey(first): "expanded", fx.posKey(second): "unresolvable"},
		fx.results("test/pay", both), "bob took the position after it was expanded")
}
