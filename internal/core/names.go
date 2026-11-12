// Package core is what every module of the notification component uses: its names, its resources and operations,
// the category catalog, its errors and checks, and Base.
package core

import (
	"time"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
)

// ID is the component's ID.
const ID = "infra/notification"

// Stream is the component's own stream: the delivery results (docs/design.md NT10).
const Stream = "infra.notification"

// DeliveryRecorded is the delivery result's event type (NT10).
const DeliveryRecorded = "infra.notification_delivery.recorded.v1"

// KeySources lists the senders the component subscribes to (NT01): component IDs, comma separated.
const KeySources = "NOTIFICATION_INTENT_SOURCES"

// Resources and operations (contract/openapi.yaml names each route's operation). Messages and preferences have no
// dimension: every one of them is the caller's own (NT08, NT09).
const (
	Messages      = "infra.notification.message"
	OpMessageRead = "infra.notification.message.read"
	OpMessageMark = "infra.notification.message.mark"
	Preferences   = "infra.notification.preference"
	OpPrefRead    = "infra.notification.preference.read"
	OpPrefManage  = "infra.notification.preference.manage"
	Rejections    = "infra.notification.rejection"
	OpRejectRead  = "infra.notification.rejection.read"
	System        = "infra.notification.system"
	// OpRecover is a full recovery's checks (be-protocol chapter 14: the recovery tool only).
	OpRecover = "infra.notification.system.recover"
	// OpResend is the redelivery of this component's stream (be-protocol P6.38: every component with streams serves
	// it; any component of the deployment may call it).
	OpResend = "infra.notification.system.resend"
)

// InApp is the only channel of the first version (NT03).
const InApp = "in_app"

// Category is one entry of the catalog (be-protocol P18.3, NT03).
type Category struct {
	Name      string
	Mandatory bool // a preference cannot turn it off
	DefaultOn bool
	Channels  []string
}

// Catalog is the category catalog, in its order.
var Catalog = []Category{
	{Name: "approval_task", DefaultOn: true, Channels: []string{InApp}},
	{Name: "due_reminder", DefaultOn: true, Channels: []string{InApp}},
	{Name: "security_alert", Mandatory: true, DefaultOn: true, Channels: []string{InApp}},
	{Name: "legal_notice", Mandatory: true, DefaultOn: true, Channels: []string{InApp}},
	{Name: "external_comm", DefaultOn: true, Channels: []string{InApp}},
	{Name: "marketing", Channels: []string{InApp}},
}

// CategoryOf answers a category of the catalog.
func CategoryOf(name string) (Category, bool) {
	for _, c := range Catalog {
		if c.Name == name {
			return c, true
		}
	}
	return Category{}, false
}

// The reasons an intent is refused (NT02; the contract's rejected intent).
const (
	NotTheSender    = "not_the_sender"
	Malformed       = "malformed"
	UnknownCategory = "unknown_category"
)

// The delivery results (NT05, NT06).
const (
	Written       = "written"
	PreferenceOff = "preference_off"
	Expired       = "expired"
	Unresolvable  = "unresolvable"
)

// Parameters.
const (
	DefaultLimit = 50
	MaxLimit     = 200
	// Batch is how many intents one delivery run claims.
	Batch = 100
	// DeliverEvery is how often the delivery job runs; a new intent also queues a run at once.
	DeliverEvery = time.Minute
	// The keeping (docs/design.md, the data).
	IntentKept   = 90 * 24 * time.Hour
	MessageKept  = 180 * 24 * time.Hour
	CollapseKept = 400 * 24 * time.Hour
	RejectedKept = 90 * 24 * time.Hour
)

// Definitions are the component's resources and operations.
func Definitions() *sdkauthz.Definitions {
	none := map[string]sdkauthz.Dimension{}
	f := map[string]sdkauthz.Field{}
	return &sdkauthz.Definitions{Component: ID,
		Resources: []sdkauthz.Resource{
			{Type: Messages, ViewOperation: OpMessageRead, Dimensions: none, Fields: f},
			{Type: Preferences, ViewOperation: OpPrefRead, Dimensions: none, Fields: f},
			{Type: Rejections, ViewOperation: OpRejectRead, Dimensions: none, Fields: f},
			{Type: System, ViewOperation: OpRecover, Dimensions: none, Fields: f},
		},
		Operations: []sdkauthz.Operation{
			{Key: OpMessageRead, ResourceType: Messages, Purpose: "read", RiskClass: "low"},
			{Key: OpMessageMark, ResourceType: Messages, Purpose: "action", RiskClass: "low", SideEffects: []string{"business_write"}},
			{Key: OpPrefRead, ResourceType: Preferences, Purpose: "read", RiskClass: "low"},
			{Key: OpPrefManage, ResourceType: Preferences, Purpose: "manage", RiskClass: "low", SideEffects: []string{"business_write"}},
			{Key: OpRejectRead, ResourceType: Rejections, Purpose: "read", RiskClass: "low"},
			{Key: OpRecover, ResourceType: System, Purpose: "read", RiskClass: "low"},
			{Key: OpResend, ResourceType: System, Purpose: "read", RiskClass: "low"},
		}}
}
