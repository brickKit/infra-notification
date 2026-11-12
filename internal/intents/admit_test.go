package intents

import (
	"encoding/json"
	"testing"

	protocol "github.com/brickKit/be-protocol/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The protocol's notification-intent vectors, admit step (P18.7-P18.9): Admit answers each as the vector does.
func TestAdmitFollowsTheProtocolsVectors(t *testing.T) {
	raw, err := protocol.Vectors.ReadFile("vectors/notification-intent.json")
	require.NoError(t, err)
	var doc struct {
		Cases []struct {
			ID   string
			Case struct {
				Step                 string
				Kind                 string
				SenderIsSource       bool   `json:"sender_is_source"`
				ShapeOK              bool   `json:"shape_ok"`
				Category             string `json:"category"`
				ExpiresAfterOccurred bool   `json:"expires_after_occurred"`
				Seen                 bool
				CollapseSeq          *int64 `json:"collapse_seq"`
				MaxSeq               *int64 `json:"max_seq"`
			}
			Expect struct {
				Answer     string
				Supersedes bool
				Delivers   bool
			}
		}
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	n := 0
	for _, v := range doc.Cases {
		if v.Case.Step != "admit" {
			continue
		}
		n++
		c := v.Case
		got := Admit(Arrival{Kind: c.Kind, SenderIsSource: c.SenderIsSource, ShapeOK: c.ShapeOK, Category: c.Category,
			ExpiresAfterOccurred: c.ExpiresAfterOccurred, Seen: c.Seen, CollapseSeq: c.CollapseSeq, MaxSeq: c.MaxSeq})
		assert.Equal(t, Admission(v.Expect), got, v.ID)
	}
	assert.Greater(t, n, 10)
}
