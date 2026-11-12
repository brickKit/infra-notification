package sources

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// NT01: the form of NOTIFICATION_INTENT_SOURCES, and the streams and types it names.
func TestTheSendersConfiguration(t *testing.T) {
	got, err := Parse(" erp/purchase , erp/payment")
	require.NoError(t, err)
	assert.Equal(t, []string{"erp/payment", "erp/purchase"}, got)
	empty, err := Parse("")
	require.NoError(t, err)
	assert.Empty(t, empty)
	for _, bad := range []string{"erp.purchase", "erp/purchase,erp/purchase", "erp/purchase,", "ERP/x"} {
		_, err := Parse(bad)
		assert.Error(t, err, bad)
	}
	assert.Equal(t, "erp.purchase", Stream("erp/purchase"))
	assert.Equal(t, "erp.purchase_notification.intent.v1", Type("erp/purchase"))
}
