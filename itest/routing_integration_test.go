//go:build integration

package itest

import (
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
		p := x.(map[string]any)
		byCat[p["category"].(string)] = p
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
	assert.Equal(t, map[string]string{carol: "written", sectest.Bob: "written", "position:" + fx.le + "/" + empty: "unresolvable"},
		fx.results("test/pay", in))
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
	assert.Equal(t, map[string]string{carol: "written"}, fx.results("test/pay", wait))
}
