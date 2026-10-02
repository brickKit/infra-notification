[English](AGENTS.md) · [中文](AGENTS.zh.md)

# infra/notification

The AI guide to developing this component. How to use it, its boundaries and contracts: BRICKKIT.md. Why it is shaped this way: `docs/design.md`. Dependencies, configuration and deployment: component.yaml.

## Code map

| Path | Owns |
|---|---|
| `backend/module/module.go` | The only entry, `New(ctx, rt)`: builds repo → service → HTTP + gRPC, reads `PG_SCHEMA` and `IM_TARGET_ADAPTERS`, starts four background loops (outbox pump, weekly and monthly partitions, consumers). Same function standalone and in a shell |
| `backend/cmd/server/main.go` | One line, `besdk.RunStandalone(module.New)` |
| `backend/cmd/migrate/main.go` | One line, `migrate.Main(migrations.FS)`: the migration container's entry (`./migrate up`) |
| `backend/internal/consumer/consumer.go` | The real entry of the business: one subscription per consumed subject; turns a task into records and dispatch events, keeps the contact snapshot, drives the delivery state machine and the retries |
| `backend/internal/repo/records.go` | `notification_records`: create, read, list (cursor = last ID), the state transitions `Mark*Tx` |
| `backend/internal/repo/preferences.go` | The two-layer preferences: `ResolvePreferredChannelsTx` (what a send uses), `GetPreferences`, `SetPreferences` (whole replacement), the critical categories and the default `[IM]` |
| `backend/internal/repo/contacts.go` | `user_contacts`: the identity snapshot, version-monotonic upserts, the disabled flag |
| `backend/internal/repo/pgarray.go` | Reading `text[]` columns as text and splitting them |
| `backend/internal/service/` | Input validation, the owner scope on REST (`ListMyRecords`), the error → gRPC status mapping (`status.go`) |
| `backend/internal/http/http.go` | REST routes, each registered with its permission key |
| `backend/internal/grpc/grpc.go` | `infra.notification.v1.NotificationService` and the proto ↔ string mappings |
| `backend/internal/partition/` | Weekly partitions of `event_outbox` / `event_inbox` (`partition.go`), monthly partitions of `notification_records` (`monthly.go`) |
| `migrations/` | SQL migrations, embedded by `migrations/embed.go` |
| `contracts/` | proto, OpenAPI, event schema |
| `gen/infra/notification/` | Generated Go code: a nested Go module, tagged on its own as gen/infra/notification/v1.x.y; never edited by hand |

| Feature | Start here | Then |
|---|---|---|
| A new event that should notify someone | `backend/internal/consumer/consumer.go` (`Start`, one handler per subject) | `backend/internal/repo/preferences.go` (a new category, critical or not), `backend/internal/consumer/consumer_test.go` |
| A new channel (email, SMS adapter) | `backend/internal/consumer/consumer.go` (`handleRetryableFailureTx`, the result subject) | `contracts/events/notification.events.json`, `backend/internal/repo/preferences.go` (`defaultGlobalChannels`) |
| Which permission key a route needs | `backend/internal/http/http.go` (`RegisterRoutes`) | `assembly.yaml` (`permissions`), `backend/internal/http/http_test.go` |
| Preference rules (layers, critical, null) | `backend/internal/repo/preferences.go` | `backend/internal/repo/preferences_test.go` |
| Delivery states and retries | `backend/internal/repo/records.go` (`Mark*Tx`) | `backend/internal/consumer/consumer.go` (`imResultHandler`), `backend/internal/repo/records_test.go` |

## Build and test

```bash
# tests run against the test database brickkit_test_db, never brickkit_db;
# at the project root, make test-db-init ID=infra/notification refreshes it and runs this component's migrations twice
export TEST_PG_DSN="postgres://postgres:<password>@localhost:5432/brickkit_test_db?sslmode=disable"
export TEST_NATS_URL="nats://localhost:4222"
make test                    # every package ends in "ok"; refuses to run without TEST_PG_DSN
go test ./... -count=1 -v | grep -c -- '--- SKIP'   # 0: no test skipped
make check-version dag-check contract-check import-scan module-check   # each prints one ✓ line
make docs-check              # "0 with errors, 0 warnings"
# migrations, as the login role:
PG_HOST=localhost PG_PORT=5432 PG_DATABASE=brickkit_test_db PG_USER=infra_notification_rw \
  PG_PASSWORD=<its password> PG_SCHEMA=infra_notification make migrate-idempotent   # ✓ 迁移幂等
```

The consumer tests subscribe and publish for real (NATS at `TEST_NATS_URL`), on test-only subjects. The HTTP test starts a local JWKS and bundle server and signs its own tokens, so it checks the real permission middleware. After `buf generate` (contracts changed), the contract package needs a new tag: see Pitfalls. On a real machine, from the project root: `make verify ID=infra/notification ROUTE=/infra/notification/notifications FOCUS=1` builds the image, starts only what this component needs, checks migration, health and the permission check, runs a focus run, and tears down.

## Design decisions

- **Events in, events out, no edges.** No `Notify` rpc and no dependency: callers raise their own events, contacts come from identity events, sending goes to whichever adapters listen.
- **Adapters say whether to retry, this component says when.** The adapter knows which platform errors are permanent; the retry count and timing live here (immediate resend, at most three retries).
- **`ACCEPTED` is not `SENT`.** An adapter first reports that the platform accepted the message, then the confirmed outcome; a record waits in `ACCEPTED` between the two.
- **Preferences are two layers with a ceiling**: the global set caps every category; critical categories never resolve to nothing; a disabled user overrides even that, in a separate check.
- **Owner scope only** on `notification_records.recipient_sub`: notifications carry document titles and amounts, so seeing someone else's would bypass the source component's data scopes.

## Pitfalls

| Never | Symptom | Why |
|---|---|---|
| Resend a dispatch with the same event version as before | `retry_count` goes up, the adapter receives nothing, no error anywhere | The adapter's inbox drops a version it has already seen for the same `record_id`; the version must be `attempt` (`publishIMDispatchTx`) |
| Put business rules in a consumer ("above amount X also notify the manager") | Nothing breaks now; the next requirement adds another branch here | When and whom to notify belongs to the component raising the event; consumers only read the recipient from the payload |
| Add EMAIL or SMS to `defaultGlobalChannels` | Records pile up in `PENDING` that nothing will ever move | No adapter consumes those dispatch events |
| Fold "the user is disabled" into the critical fallback of `ResolvePreferredChannelsTx` | A disabled user still receives critical notifications, and nobody thinks to look there | Two different decisions: the preference fallback, then the disabled override in the consumer |
| Scan a `text[]` column into `*[]string`, or pass a nil slice for a `NOT NULL` array column | `unsupported Scan`; or a `NOT NULL` violation and a `500` | pgx's `database/sql` interface does not decode arrays (`pgarray.go`); a nil slice is sent as `NULL` |
| Call a consumer test's handler directly or use a production subject in it | Tests pass or fail depending on which containers run on the machine | Production subjects are broadcast to running containers too; the tests use `testSubject` and a real subscription |
| Change a comment or option in `contracts/infra/notification/v1/notification.proto` without tagging the contract package | Builds pass locally (the `replace` hides it); a shell fetching `infra-notification/v2` compiles against the old `gen/` from the tag `go.mod` requires | Any change under `gen/infra/notification/` needs a new tag `gen/infra/notification/v1.x.y` and the root `go.mod` requiring it |

## Before changing code

1. Am I deciding *whether* or *whom* to notify? That belongs to the component raising the event; here only "take the recipient from the payload".
2. Does a resent or new dispatch carry a version greater than the last one for that `record_id`?
3. Does a new channel have an adapter? If not, it stays out of the default global set.
4. Contract change? Append only: a new field, a new rpc, a new event. Never remove or retype a field, rpc, path or event subject. Did `gen/` change? Then the contract package needs a new tag and `go.mod` must require it.
5. A new REST route is registered with `besdk.GET` / `PUT` and a permission key from `assembly.yaml`; a list a user sees is filtered by `recipient_sub`, with a test that someone else's rows stay invisible.
6. New rule → write the failing test first (real database and real NATS), then the code.
7. Bump `metadata.version` before the first change after a release; update BRICKKIT.md and `docs/design.md` in the same commit as the code.

<!-- brickkit:managed:begin lang=en -->
<!-- maintained by brickkit (init, add, remove, upgrade, skills update): edits between these markers are overwritten -->

## BrickKit

This is a BrickKit component: `component.yaml` is all the platform reads. The rules it relies on:

- `configSchema` keys are the environment variable names the code reads. Never use a reserved name: `COMPONENT_ID`, `COMPONENT_VERSION`, `PORT`, `BRICKKIT_SERVED_MEMBERS`, `BRICKKIT_SERVED_MEMBERS_CONFIG`, or any `*_ENDPOINT`.
- Dependencies are exact versions. A dependency's address arrives as `<ID>_ENDPOINT`; an optional dependency that is absent has no variable at all, so read it with a fallback.
- `/healthz` checks only this process, never a dependency. The migration command runs from the same image and must fail on an argument it does not know.
- `BRICKKIT.md` travels to every project that uses this component and is read there without the repository: keep it in step with the code, with no relative links.
- Release: raise `metadata.version`, commit, push, `brickkit release`. `brickkit lint` checks the manifest and these docs — inside a project, run in this directory, it checks only this component (`--all` for the whole project).
- The full rules are in the `brickkit-component` skill (`.claude/skills/brickkit-component/SKILL.md` at the root of the project or repository where skills are installed; `brickkit skills update` installs it); for flags ask `brickkit <command> --help`; BrickKit's own documentation is `brickkit docs`.
<!-- brickkit:managed:end -->
