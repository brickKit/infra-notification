# infra/notification

## Purpose

Turns "someone should be told" into messages: it consumes notification-worthy events from other components, decides from each recipient's channel preferences which channels a notification goes to, records every message and its delivery state, and hands the actual sending to channel adapters through dispatch events. Keys, SDKs and rate limits of external platforms stay in the adapters; retry timing stays here.

**Owns**

- Notification records (`notification_records`): recipient, category, channel, title and body as sent, delivery state (`PENDING`, `ACCEPTED`, `RETRYING`, then `SENT`, `FAILED_PERMANENT` or `SUPPRESSED`), retry count, the adapter's task ID.
- Channel preferences per user, in two layers: a global channel set and per-category channel sets, with critical categories that cannot be switched off (today `workflow_task`).
- A snapshot of each recipient's contact details (phone, email, display name, disabled), kept from identity events.
- Retrying: an adapter says whether a failure is retryable; this component decides to resend, up to three retries.
- The events `infra.notification.dispatch.im.v1`, `infra.notification.dispatch.email.v1`, `infra.notification.sent.v1`, `infra.notification.failed.v1`.

**Does not own**

- Calling DingTalk, email or SMS: the `integration/*` channel adapters (for IM, `integration/im-dingtalk`), which consume the dispatch events and report back.
- When something deserves a notification and what it says: the component that raises it (today `infra/workflow` with its tasks). This component only takes the recipient and the title from the event.
- Who a user is and their authoritative phone number: the identity component (`infra/iam-casdoor`); this component holds a snapshot only.
- Rendering the notification list or the preference page: the frontend.

## Before you deploy

- **PostgreSQL**: a schema `infra_notification`, plus `infra_notification_archive` if your schema convention creates one (this component never writes to it). A login role `infra_notification_rw` with `USAGE` and `CREATE` on `infra_notification`, and its password. The migration runs as this role and creates the tables, so the role owns them; that matters because the running component creates partitions itself (monthly for `notification_records`, three months ahead; weekly for `event_outbox` / `event_inbox`, four weeks ahead), which needs ownership. The migrations name the role `infra_notification_rw` literally, so keep the default schema. BrickKit creates none of this; in the BrickEnterprise assembly project `make dev-env` writes the password into `.env` and `make db-init` creates the schemas, role and grants.
- **NATS** reachable at `NATS_URL` when the component starts (it exits if it cannot connect). Events are consumed through plain NATS subscriptions: an event published while this component is not running is not delivered to it later.
- **Authorization** (`infra/authz`) and **identity** (`infra/iam-casdoor`, or any IAM serving a JWKS) reachable at `AUTHZ_BUNDLE_URL` and `IAM_JWKS_URL` for the REST routes to answer anything but errors. They are configuration, not dependencies: the component starts without them.
- To deliver anything, at least one IM channel adapter (`integration/im-dingtalk`) running, and its name listed in `IM_TARGET_ADAPTERS`. Without one, records stay `PENDING`; nothing fails.
- Recipients need a phone number in their identity record: a recipient without one gets a `FAILED_PERMANENT` record ("收件人手机号为空") instead of a message.

## Dependencies

None. This component lives on the event graph only: it calls no component and no component calls it synchronously. Contact details arrive as identity events, not through calls to the identity component; dispatch events go to whichever adapters are installed, without knowing which ones; tasks arrive as `infra/workflow` events. Not installing it costs only the notifications: every business action still succeeds.

The authorization bundle and the JWKS are fetched from the URLs in the configuration, not through a dependency edge. Without them every protected route fails closed: no or invalid token → `401`; `IAM_JWKS_URL` empty or unreachable → `403`; the bundle never fetched yet → `503`; a valid user without the permission key → `403`. `/healthz` stays `200` throughout: it reports only that this process is alive.

## Configuration

| Variable | Meaning |
|---|---|
| `PG_HOST` | PostgreSQL host. Usually the project's shared value (`$var:PG_HOST`). |
| `PG_PORT` | PostgreSQL port; default `5432`. |
| `PG_DATABASE` | The database holding the `infra_notification` schema (`$var:PG_DATABASE`). |
| `PG_USER` | The login role, `infra_notification_rw` as a literal. Inside a shell the shell logs in with its own role and switches to this one per transaction (`SET LOCAL ROLE`), so the role name must be `<PG_SCHEMA>_rw`. |
| `PG_PASSWORD` | Password of `PG_USER`. Secret: write `${INFRA_NOTIFICATION_DB_PASSWORD}` (or your secret store's reference), never the value. |
| `PG_SCHEMA` | Schema of all tables, the outbox and the migration state table (`schema_migrations_infra_notification`); default `infra_notification`. Write the literal anyway so every component's schema is visible in one place. |
| `NATS_URL` | NATS server the component subscribes to and the outbox pump publishes to (`$var:NATS_URL`). |
| `OTEL_BASE_URL` | OpenTelemetry collector base URL; empty (the default) exports nothing. |
| `AUTHZ_BUNDLE_URL` | URL of the authorization bundle the permission check polls, for example `http://infra-authz-2-0-0:8223/authz/bundle`. Required: without it every protected route answers `503`. Keep it in step with the authz version the project runs. |
| `IAM_JWKS_URL` | URL of the JWKS used to verify user tokens locally, for example `http://infra-iam-casdoor-2-0-0:8200/.well-known/jwks.json`. Required: without it every protected route answers `403`. |
| `IM_TARGET_ADAPTERS` | Comma-separated names of the IM adapters a dispatch is addressed to, copied into `target_adapters` of every `infra.notification.dispatch.im.v1`; default `dingtalk`. Every IM adapter receives every dispatch and handles only those that name it, so a name missing here means that adapter ignores everything. Leave it at the default while `integration/im-dingtalk` is the only IM adapter; add a name when you install a second one. |

## Contracts

- `contracts/infra/notification/v1/notification.proto` — gRPC `infra.notification.v1.NotificationService` for other components: `BatchGetRecords` (missing IDs are left out), `ListRecords` (cursor paging; filters by category and status, no user scoping), `GetPreferences` / `SetPreferences` (by `sub`; `SetPreferences` replaces the whole preference set and is idempotent by nature). No `Notify` rpc: the only way in is an event. The Go package is the separate module `github.com/brickKit/infra-notification/gen/infra/notification`.
- `contracts/notification.openapi.yaml` — REST under `/infra/notification`, every route behind a permission key: `GET /notifications` (`infra.notification.view`; the caller's own notifications only; `category`, `status`, `cursor`, `page_size`), `GET /preferences` (`infra.notification.view`; the caller's own preferences, defaults filled in), `PUT /preferences` (`infra.notification.preference.edit`; replaces the caller's preferences: an omitted or `null` `global_channels` follows the default `[IM]`, an omitted or `null` category follows the global set, an empty list switches that layer off; emptying a critical category answers `400`), `GET /admin/records` (`infra.notification.admin`; any recipient, optional `recipient_sub`, for troubleshooting).
- `contracts/events/notification.events.json` — published through the outbox: `infra.notification.dispatch.im.v1` (per IM record; `record_id`, `attempt`, `target_adapters`, `recipient_phone`, `title`, `body`; the event version equals `attempt`, 1 for the first send and one more per retry), `infra.notification.dispatch.email.v1` (the same with `recipient_email`; no adapter consumes it yet), `infra.notification.sent.v1` and `infra.notification.failed.v1` (final outcome, for audit and monitoring). Consumed: `infra.workflow.task.created.v1` (a new task → notify the assignee), `infra.iam.user.created.v1` / `infra.iam.user.updated.v1` / `infra.iam.user.disabled.v1` (the contact snapshot; a disabled user receives nothing, even for a critical category), `integration.im.result.v1` (the adapters' delivery results: `ACCEPTED`, then `CONFIRMED` with success or a retryable / permanent failure).
- `assembly.yaml` — this project's metadata: the three permission keys above, the menu entry, the edge route `/infra/notification/**`, the schema and role, and the `owner` data scope on `notification_records.recipient_sub`.

## Shell declaration

Not a shell. It can be hosted in a Go shell (in the BrickEnterprise project, `be/go-infra`) or run on its own; the code is the same either way.
