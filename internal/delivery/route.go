package delivery

import core "github.com/brickKit/infra-notification/v3/internal/core"

// Route is one principal's result on one channel (be-protocol P18.3, P18.6; NT05, NT07; the vector
// notification-intent's route step): preference is "on", "off" or "" (no row: the category's default).
func Route(category, preference string, expired bool) string {
	c, _ := core.CategoryOf(category)
	switch {
	case c.Mandatory: // in-app is where a mandatory category falls back to: written even when expired (P18.6)
		return core.Written
	case expired:
		return core.Expired
	case preference == "on", preference == "" && c.DefaultOn:
		return core.Written
	}
	return core.PreferenceOff
}
