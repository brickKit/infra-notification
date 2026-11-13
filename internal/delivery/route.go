package delivery

import core "github.com/brickKit/infra-notification/v3/internal/core"

// Route is one principal's result on one channel (be-protocol P18.3, P18.6, P20.3, P20.4; NT05, NT07, NT13; the
// vector notification-intent's route step): preference is "on", "off" or "" (no row: the category's default).
// In-app is written; an external channel's delivery is pending until it is handed over.
func Route(category, channel, preference string, expired bool) string {
	c, _ := core.CategoryOf(category)
	switch {
	case c.Mandatory && channel == core.InApp: // where a mandatory category falls back to: written even when expired
		return core.Written
	case expired:
		return core.Expired
	case preference == "off", preference == "" && !c.DefaultOn:
		return core.PreferenceOff
	case channel != core.InApp:
		return core.Pending
	}
	return core.Written
}
