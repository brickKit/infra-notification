// Package notification is the notification component (assembly protocol 10, be-protocol chapter 18): it takes the
// notification intents of the deployment's senders in, routes them by category and preference, expands positions to
// their holders, and writes the in-app messages, its first channel.
package notification

import (
	"embed"
	"io/fs"

	core "github.com/brickKit/infra-notification/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/component"
	"github.com/brickKit/be-sdk-go/v3/config"
	"github.com/brickKit/be-sdk-go/v3/events"
	"github.com/brickKit/be-sdk-go/v3/runtime"
)

// Identity.
const (
	Version  = "3.0.4"
	GRPCPort = 9202
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func migrations() fs.FS {
	sub, _ := fs.Sub(migrationFiles, "migrations")
	return sub
}

// Keys are the component's own configuration keys.
func Keys() []config.Key { return []config.Key{{Name: core.KeySources, Type: config.String}} }

// Own describes the own keys for the manifest's configSchema.
var Own = map[string]string{
	core.KeySources: "the senders whose notification intents are taken in: component IDs, comma separated (e.g. " +
		"erp/purchase,erp/payment); unset: none",
}

// Definition is the component as the launcher and the shells take it.
func Definition() component.Definition {
	return component.Definition{ID: core.ID, Version: Version, Keys: Keys(), Migrations: migrations(), GRPCPort: GRPCPort,
		Subscribes: true, Streams: []events.Stream{{Name: core.Stream, Partitions: 4}}, Authz: core.Definitions(),
		Recovery: core.OpRecover, Resend: core.OpResend,
		New: func(rt *runtime.Runtime) (component.Module, error) { return New(rt) }}
}
