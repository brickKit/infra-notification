package delivery

import (
	"encoding/json"
	"testing"

	protocol "github.com/brickKit/be-protocol/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The protocol's notification-intent vectors, route step (P18.3, P18.6, P20.4): Route answers each as the vector does.
func TestRouteFollowsTheProtocolsVectors(t *testing.T) {
	raw, err := protocol.Vectors.ReadFile("vectors/notification-intent.json")
	require.NoError(t, err)
	var doc struct {
		Cases []struct {
			ID   string
			Case struct {
				Step, Category, Channel, Preference string
				Expired                             bool
			}
			Expect struct{ Result string }
		}
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	n := 0
	for _, v := range doc.Cases {
		if v.Case.Step != "route" {
			continue
		}
		n++
		pref := v.Case.Preference
		if pref == "unset" {
			pref = ""
		}
		ch := v.Case.Channel
		if ch == "external" {
			ch = "dingtalk" // any configured external channel
		}
		assert.Equal(t, v.Expect.Result, Route(v.Case.Category, ch, pref, v.Case.Expired), v.ID)
	}
	assert.Greater(t, n, 10)
}
