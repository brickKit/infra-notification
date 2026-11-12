package notification

import "github.com/brickKit/be-sdk-go/v3/recovery"

// RecoveryChecks registers what a full recovery asks this member (be-protocol chapter 14): nothing of its own. Its
// intents come again by redelivery from their senders (the SDK's own checks cover the consumer's progress); it audits
// no business object and numbers nothing.
func (m *Module) RecoveryChecks(c *recovery.Checks) error { return nil }
