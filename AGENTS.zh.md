[English](AGENTS.md) · [中文](AGENTS.zh.md)

# infra/notification

开发本组件的 AI 指南。用法、边界与契约：BRICKKIT.md；为什么长这样：`docs/design.zh.md`；依赖、配置与部署：component.yaml。

## 代码地图

| 路径 | 负责 |
|---|---|
| `backend/module/module.go` | 唯一入口 `New(ctx, rt)`：组装 repo → service → HTTP + gRPC，读 `PG_SCHEMA` 与 `IM_TARGET_ADAPTERS`，起四个后台循环（Outbox 推送、周分区、月分区、消费者）。单跑与进外壳是同一个函数 |
| `backend/cmd/server/main.go` | 一行 `besdk.RunStandalone(module.New)` |
| `backend/cmd/migrate/main.go` | 一行 `migrate.Main(migrations.FS)`：迁移容器的入口（`./migrate up`） |
| `backend/internal/consumer/consumer.go` | 业务真正的入口：每条消费的 subject 一个订阅；把待办变成记录与 dispatch 事件，维护联系方式快照，推进投递状态机与重试 |
| `backend/internal/repo/records.go` | `notification_records`：建、读、列表（游标 = 最后一条的 ID）、状态流转 `Mark*Tx` |
| `backend/internal/repo/preferences.go` | 两层偏好：`ResolvePreferredChannelsTx`（发送时用的解析）、`GetPreferences`、`SetPreferences`（整份替换）、critical 类别与默认 `[IM]` |
| `backend/internal/repo/contacts.go` | `user_contacts`：身份快照，按版本单调 upsert，停用标记 |
| `backend/internal/repo/pgarray.go` | `text[]` 列按文本读出再切分 |
| `backend/internal/service/` | 入参校验、REST 上的 owner 范围（`ListMyRecords`）、错误 → gRPC 状态码映射（`status.go`） |
| `backend/internal/http/http.go` | REST 路由，每条都带权限键注册 |
| `backend/internal/grpc/grpc.go` | `infra.notification.v1.NotificationService` 与 proto ↔ 字符串的映射 |
| `backend/internal/partition/` | `event_outbox` / `event_inbox` 的周分区（`partition.go`）、`notification_records` 的月分区（`monthly.go`） |
| `migrations/` | SQL 迁移，由 `migrations/embed.go` 嵌入 |
| `contracts/` | proto、OpenAPI、事件 schema |
| `gen/infra/notification/` | 生成的 Go 代码：独立的嵌套 Go 模块，单独打 tag gen/infra/notification/v1.x.y；绝不手改 |

| 功能 | 从这里看 | 然后 |
|---|---|---|
| 新的一类事件要通知人 | `backend/internal/consumer/consumer.go`（`Start`，每条 subject 一个 handler） | `backend/internal/repo/preferences.go`（新类别，是否 critical）、`backend/internal/consumer/consumer_test.go` |
| 新通道（邮件、短信适配器） | `backend/internal/consumer/consumer.go`（`handleRetryableFailureTx`、结果事件的 subject） | `contracts/events/notification.events.json`、`backend/internal/repo/preferences.go`（`defaultGlobalChannels`） |
| 某条路由要哪个权限键 | `backend/internal/http/http.go`（`RegisterRoutes`） | `assembly.yaml`（`permissions`）、`backend/internal/http/http_test.go` |
| 偏好规则（两层、critical、null） | `backend/internal/repo/preferences.go` | `backend/internal/repo/preferences_test.go` |
| 投递状态与重试 | `backend/internal/repo/records.go`（`Mark*Tx`） | `backend/internal/consumer/consumer.go`（`imResultHandler`）、`backend/internal/repo/records_test.go` |

## 构建与测试

```bash
# 测试连测试库 brickkit_test_db，绝不连 brickkit_db；
# 项目根 make test-db-init ID=infra/notification 刷新它，并把本组件的迁移连跑两次
export TEST_PG_DSN="postgres://postgres:<密码>@localhost:5432/brickkit_test_db?sslmode=disable"
export TEST_NATS_URL="nats://localhost:4222"
make test                    # 每个包都以 "ok" 结尾；没设 TEST_PG_DSN 时拒绝运行
go test ./... -count=1 -v | grep -c -- '--- SKIP'   # 0：没有测试被跳过
make check-version dag-check contract-check import-scan module-check   # 各打印一行 ✓
make docs-check              # "0 with errors, 0 warnings"
# 迁移，以登录角色运行：
PG_HOST=localhost PG_PORT=5432 PG_DATABASE=brickkit_test_db PG_USER=infra_notification_rw \
  PG_PASSWORD=<它的密码> PG_SCHEMA=infra_notification make migrate-idempotent   # ✓ 迁移幂等
```

消费者测试真订阅、真发布（NATS 在 `TEST_NATS_URL`），用测试私有的 subject。HTTP 测试在本地起 JWKS 与 bundle 服务器、自己签 token，测的是真实的权限中间件。`buf generate` 之后（契约变了），契约包要打新 tag：见易错点。真机：在项目根 `make verify ID=infra/notification ROUTE=/infra/notification/notifications FOCUS=1`，构建镜像、只起本组件需要的东西、核对迁移、健康检查与权限判定、跑一次 focus，然后收尾。

## 设计取舍

- **事件进、事件出，没有依赖边。** 没有 `Notify` rpc，也没有依赖：调用方发自己的事件，联系方式来自身份事件，发送交给在听的适配器。
- **适配器判断该不该重试，本组件决定什么时候重试。** 适配器认得哪些平台错误是永久的；重试次数与时机在这里（立即重发，最多重试三次）。
- **`ACCEPTED` 不是 `SENT`。** 适配器先报告平台受理了消息，再报告确认后的结果；两者之间记录停在 `ACCEPTED`。
- **偏好两层、有天花板**：全局集合是每个类别的上限；critical 类别永远解析不出空集；停用用户在另一处单独判断，连这一条也盖过。
- **只有 owner 范围**，在 `notification_records.recipient_sub` 上：通知里带着单据标题与金额，看得到别人的通知就绕过了来源组件的数据范围。

## 易错点

| 不许 | 症状 | 原因 |
|---|---|---|
| 重发 dispatch 时沿用上一次的事件版本 | `retry_count` 涨了，适配器什么都没收到，哪里都没有报错 | 适配器的 inbox 丢掉同一个 `record_id` 已经见过的版本；版本必须是 `attempt`（`publishIMDispatchTx`） |
| 在消费者里写业务规则（"金额大于 X 就抄送主管"） | 现在什么都不坏；下一个需求就会在这里再加一个分支 | 什么时候通知、通知谁归发事件的组件；消费者只从 payload 里取收件人 |
| 往 `defaultGlobalChannels` 里加 EMAIL 或 SMS | `PENDING` 记录越积越多，永远没人推进 | 没有适配器消费这些 dispatch 事件 |
| 把"用户已停用"合并进 `ResolvePreferredChannelsTx` 的 critical 兜底 | 停用的用户照样收到 critical 通知，而且没人想到去那里查 | 两个不同的判断：先偏好兜底，再在消费者里用停用覆盖 |
| 把 `text[]` 列 Scan 进 `*[]string`，或给 `NOT NULL` 的数组列传 nil 切片 | `unsupported Scan`；或者违反 `NOT NULL`、请求 `500` | pgx 的 `database/sql` 接口不解数组（`pgarray.go`）；nil 切片按 `NULL` 发送 |
| 消费者测试直接调 handler，或者用生产 subject | 测试过不过取决于这台机器上跑着哪些容器 | 生产 subject 也会广播给在跑的容器；测试用 `testSubject` 与真订阅 |
| 改了 `contracts/infra/notification/v1/notification.proto` 的注释或选项却不给契约包打 tag | 本地构建通过（`replace` 把问题盖住了）；外壳拉取 `infra-notification/v2` 时对着 `go.mod` 所 require 的 tag 里的旧 `gen/` 编译 | `gen/infra/notification/` 下任何变化都要打新 tag `gen/infra/notification/v1.x.y`，根 `go.mod` 要 require 它 |

## 改代码前自查

1. 我是不是在决定*要不要*通知、*通知谁*？那归发事件的组件；这里只做"从 payload 里取收件人"。
2. 重发的或新的 dispatch，版本是不是比这个 `record_id` 上一次的大？
3. 新通道有没有适配器？没有就不进默认的全局集合。
4. 契约变更？只增：新字段、新 rpc、新事件。绝不删除或改类型：字段、rpc、路径、事件 subject。`gen/` 变了吗？那契约包要打新 tag，`go.mod` 要 require 它。
5. 新 REST 路由用 `besdk.GET` / `PUT` 加 `assembly.yaml` 里的权限键注册；用户看到的列表按 `recipient_sub` 过滤，并有"别人的行看不到"的测试。
6. 新规则 → 先写跑红的测试（真实数据库与真实 NATS），再写代码。
7. 发布之后的第一处改动之前先升 `metadata.version`；BRICKKIT.md 与 `docs/design.md` 和代码在同一个提交里更新。
