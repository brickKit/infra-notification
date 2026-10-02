[English](README.md) · [中文](README.zh.md)

# infra/notification

Takes notification intents from business components and routes them to the installed channel adapters by each user's preferences.

## Use it in a project

```bash
brickkit add infra/notification@2.0.0
brickkit up
```

Prepare first: see "Before you deploy" in BRICKKIT.md (a PostgreSQL schema and login role, NATS, the authorization and JWKS URLs, and an IM adapter such as `integration/im-dingtalk` to actually deliver).

## Documentation

| To find out | Read |
|---|---|
| What it does and does not do, how to configure it, what to prepare | [BRICKKIT.md](BRICKKIT.md) |
| Its gRPC, REST and event contracts | [contracts/](contracts/) |
| Why it is designed this way: boundary, preference model, delivery states, open questions | [docs/design.md](docs/design.md) |
| What it depends on (nothing) and its configuration keys | [component.yaml](component.yaml) |
| How to develop it: code map, tests, pitfalls | [AGENTS.md](AGENTS.md) |

## Development

Go 1.25, Gin, `database/sql` with pgx, golang-migrate through be-sdk-go. Tests need a real PostgreSQL (`TEST_PG_DSN`) and NATS (`TEST_NATS_URL`); the commands and what success looks like are in AGENTS.md, section "Build and test". Inside the BrickEnterprise project, `make verify ID=infra/notification FOCUS=1` at the project root runs it for real, in a container and as a local process, and tears down afterwards.
