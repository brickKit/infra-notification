package notification

import (
	"context"
	"fmt"
	"slices"
	"time"

	core "github.com/brickKit/infra-notification/v3/internal/core"
	delivery "github.com/brickKit/infra-notification/v3/internal/delivery"
	inbox "github.com/brickKit/infra-notification/v3/internal/inbox"
	intents "github.com/brickKit/infra-notification/v3/internal/intents"
	preferences "github.com/brickKit/infra-notification/v3/internal/preferences"
	sources "github.com/brickKit/infra-notification/v3/internal/sources"

	"github.com/brickKit/be-sdk-go/v3/events"
	"github.com/brickKit/be-sdk-go/v3/flow"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/runtime"
	"github.com/brickKit/be-sdk-go/v3/work"
)

// Module is the notification component as the launcher and the shells run it: it only assembles the business modules
// under internal/ (their map is internal/AGENTS.md).
type Module struct {
	b         *core.Base
	counters  *delivery.Counters
	employees *flow.Employees // nil without mdm/employee
}

// New builds the module from its runtime: the deployment's senders and external channels (a malformed
// configuration stops the member, NT01, NT13) and, when the deployment has mdm/employee, the employee projection the positions expand against (NT06).
func New(rt *runtime.Runtime) (*Module, error) {
	s, err := sources.Parse(rt.Config.String(core.KeySources))
	if err != nil {
		return nil, err
	}
	chs, err := sources.Channels(rt.Config.String(core.KeyChannels))
	if err != nil {
		return nil, err
	}
	for _, ch := range chs { // a channel member must be an optional dependency: its address comes from the platform
		if !slices.Contains(core.ChannelMembers, ch.Component) {
			return nil, fmt.Errorf("%s: %s is not a channel member this component declares", core.KeyChannels, ch.Component)
		}
	}
	m := &Module{b: &core.Base{RT: rt, Senders: s, Channels: chs}, counters: delivery.NewCounters(rt.Metrics)}
	if e, ok := flow.NewEmployees(rt); ok {
		m.employees, m.b.Directory = e, e
		e.OnChange(delivery.EmployeesChanged(intents.JobDeliverNow))
	}
	return m, nil
}

// UseDirectory replaces the directory the positions expand against (the integration tests: the employee projection
// needs a real mdm/employee).
func (m *Module) UseDirectory(d core.Directory) { m.b.Directory = d }

// Start runs the employee projection (NT06).
func (m *Module) Start(ctx context.Context) error {
	if m.employees == nil {
		return nil
	}
	return m.employees.Start(ctx)
}

// Routes registers the user surface (contract/openapi.yaml).
func (m *Module) Routes(r *httpx.Routes) error {
	h := func(f func(*core.Base, *httpx.Context) error) httpx.Handler {
		return func(c *httpx.Context) error { return f(m.b, c) }
	}
	r.Add(httpx.Route{Method: "GET", Path: "/api/notification/messages", Operation: core.OpMessageRead, Handle: h(inbox.Mine)})
	r.Add(httpx.Route{Method: "POST", Path: "/api/notification/messages/:id/read", Operation: core.OpMessageMark,
		Handle: h(inbox.MarkRead)})
	r.Add(httpx.Route{Method: "POST", Path: "/api/notification/messages/read-all", Operation: core.OpMessageMark,
		Handle: h(inbox.MarkAllRead)})
	r.Add(httpx.Route{Method: "GET", Path: "/api/notification/preferences", Operation: core.OpPrefRead,
		Handle: h(preferences.Mine)})
	r.Add(httpx.Route{Method: "PUT", Path: "/api/notification/preferences/:category/:channel", Operation: core.OpPrefManage,
		Handle: h(preferences.Set)})
	r.Add(httpx.Route{Method: "GET", Path: "/api/notification/rejected-intents", Operation: core.OpRejectRead,
		Handle: h(intents.Rejected)})
	return nil
}

// Subscriptions are the senders' streams (NT01) and the employee stream when the deployment has it (NT06).
func (m *Module) Subscriptions(s *events.Subscriptions) error {
	if m.b == nil { // the contract tests build the module without a runtime
		return nil
	}
	for _, sub := range intents.Subs(m.b, intents.OnEvent) {
		s.Add(sub)
	}
	if m.employees != nil {
		s.Add(m.employees.Sub())
	}
	return nil
}

// Jobs declares the background work (protocol 01): the delivery (queued by each new intent, and every minute as the
// floor) and the daily purge.
func (m *Module) Jobs(j *work.Jobs) error {
	deliver := func(ctx context.Context, r *work.Run) error { return delivery.Deliver(m.b, m.counters, ctx, r) }
	j.Add(work.Job{Name: intents.JobDeliverNow, Kind: work.Queue, Effect: work.Local, MaxAttempts: work.Unlimited,
		Handle: deliver})
	j.Add(work.Job{Name: delivery.JobDeliver, Kind: work.Singleton, Effect: work.Local, Interval: core.DeliverEvery,
		MaxAttempts: work.Unlimited, Handle: deliver})
	if len(m.b.Channels) > 0 { // the external deliveries, handed over by row outside any transaction (NT13)
		external := func(ctx context.Context, r *work.Run) error { return delivery.DeliverExternal(m.b, m.counters, ctx, r) }
		j.Add(work.Job{Name: delivery.JobExternal, Kind: work.Singleton, Effect: work.IdempotentExternal,
			Interval: 15 * time.Second, MaxAttempts: work.Unlimited, Handle: external})
		j.Add(work.Job{Name: delivery.JobExternalNow, Kind: work.Queue, Effect: work.IdempotentExternal,
			MaxAttempts: work.Unlimited, Handle: external})
	}
	j.Add(work.Job{Name: delivery.JobPurge, Kind: work.Singleton, Effect: work.Local, Interval: 24 * time.Hour,
		MaxAttempts: work.Unlimited, Handle: func(ctx context.Context, r *work.Run) error { return delivery.Purge(m.b, ctx, r) }})
	return nil
}
