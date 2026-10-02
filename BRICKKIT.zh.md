# infra/notification

## 组件定位

把"该告诉某人一件事"变成消息：消费别的组件发出的、值得通知的事件，按每个收件人的通道偏好决定一条通知走哪些通道，记下每条消息及其投递状态，真正的发送经 dispatch 事件交给通道适配器。外部平台的密钥、SDK、限流留在适配器里；重试的时机留在这里。

**拥有**

- 通知记录（`notification_records`）：收件人、类别、通道、发出的标题与正文、投递状态（`PENDING`、`ACCEPTED`、`RETRYING`，终态 `SENT`、`FAILED_PERMANENT` 或 `SUPPRESSED`）、重试次数、适配器返回的任务 ID。
- 每个用户的通道偏好，分两层：全局通道集合与按类别的通道集合；critical 类别关不掉（目前是 `workflow_task`）。
- 每个收件人联系方式的快照（手机号、邮箱、显示名、是否停用），由身份事件维护。
- 重试：适配器判断一次失败能不能重试，本组件决定重发，最多重试三次。
- 事件 `infra.notification.dispatch.im.v1`、`infra.notification.dispatch.email.v1`、`infra.notification.sent.v1`、`infra.notification.failed.v1`。

**不拥有**

- 调钉钉、邮件、短信的那一下：归 `integration/*` 通道适配器（IM 是 `integration/im-dingtalk`），它们消费 dispatch 事件并回报结果。
- 什么事值得通知、通知里说什么：归发起它的组件（目前是 `infra/workflow` 的待办）。本组件只从事件里取收件人和标题。
- 用户是谁、权威的手机号：归身份组件（`infra/iam-casdoor`），本组件只持快照。
- 通知列表与偏好页面的展示：归前端。

## 部署前准备

- **PostgreSQL**：schema `infra_notification`；如果你的 schema 约定会建 `infra_notification_archive` 也可以建（本组件从不写它）。登录角色 `infra_notification_rw`，在 `infra_notification` 上有 `USAGE` 与 `CREATE`，以及它的密码。迁移以这个角色运行并建表，所以表归它所有——这一点有用：运行中的组件会自己建分区（`notification_records` 按月、提前三个月；`event_outbox` / `event_inbox` 按周、提前四周），这需要表的所有权。迁移里按字面写了角色名 `infra_notification_rw`，所以 schema 保持默认值。brickKit 不建这些；在 BrickEnterprise 装配项目里，`make dev-env` 把密码写进 `.env`，`make db-init` 建 schema、角色与授权。
- **NATS** 在组件启动时可经 `NATS_URL` 访问（连不上就退出）。事件经普通的 NATS 订阅消费：本组件没在跑的时候发布的事件，之后不会再投递给它。
- **权限**（`infra/authz`）与**身份**（`infra/iam-casdoor`，或任何提供 JWKS 的 IAM）可经 `AUTHZ_BUNDLE_URL`、`IAM_JWKS_URL` 访问，REST 路由才会返回错误以外的结果。它们是配置不是依赖：没有它们组件照样启动。
- 要真的发出消息，至少要有一个 IM 通道适配器（`integration/im-dingtalk`）在跑，并且它的名字在 `IM_TARGET_ADAPTERS` 里。没有适配器时记录停在 `PENDING`，不会失败。
- 收件人在身份记录里要有手机号：没有手机号的收件人得到一条 `FAILED_PERMANENT` 记录（"收件人手机号为空"），而不是一条消息。

## 依赖说明

无。本组件只活在事件图上：不调用任何组件，也没有组件同步调它。联系方式经身份事件进来，不调身份组件；dispatch 事件发给装了的任何适配器，不需要知道装了哪些；待办经 `infra/workflow` 的事件进来。不装本组件只损失通知：每个业务动作照样成功。

权限 bundle 与 JWKS 从配置里的 URL 拉取，不是依赖边。没有它们时每条受保护路由都失败关闭：没有或无效的 token → `401`；`IAM_JWKS_URL` 为空或不可达 → `403`；还没拉到过 bundle → `503`；合法用户但没有这个权限键 → `403`。`/healthz` 始终 `200`：它只报告本进程活着。

## 配置指南

| 变量 | 含义 |
|---|---|
| `PG_HOST` | PostgreSQL 主机。通常写项目的共享值（`$var:PG_HOST`）。 |
| `PG_PORT` | PostgreSQL 端口；默认 `5432`。 |
| `PG_DATABASE` | 放 `infra_notification` schema 的数据库（`$var:PG_DATABASE`）。 |
| `PG_USER` | 登录角色，字面量 `infra_notification_rw`。在外壳里由外壳用自己的角色登录，每个事务切到这个角色（`SET LOCAL ROLE`），所以角色名必须是 `<PG_SCHEMA>_rw`。 |
| `PG_PASSWORD` | `PG_USER` 的密码。密钥：写 `${INFRA_NOTIFICATION_DB_PASSWORD}`（或你的密钥存储的引用），绝不写值。 |
| `PG_SCHEMA` | 全部表、Outbox 与迁移状态表（`schema_migrations_infra_notification`）所在的 schema；默认 `infra_notification`。仍然写出字面量，这样每个组件的 schema 在一处都看得见。 |
| `NATS_URL` | 组件订阅事件、Outbox 推送发布事件用的 NATS（`$var:NATS_URL`）。 |
| `OTEL_BASE_URL` | OpenTelemetry collector 的基础地址；为空（默认）时什么都不导出。 |
| `AUTHZ_BUNDLE_URL` | 权限判定轮询的权限 bundle 地址，例如 `http://infra-authz-2-0-0:8223/authz/bundle`。必填：没有它每条受保护路由都回 `503`。要与项目里跑的 authz 版本保持一致。 |
| `IAM_JWKS_URL` | 本地验签用户 token 用的 JWKS 地址，例如 `http://infra-iam-casdoor-2-0-0:8200/.well-known/jwks.json`。必填：没有它每条受保护路由都回 `403`。 |
| `IM_TARGET_ADAPTERS` | 一条 dispatch 点名交给哪些 IM 适配器，逗号分隔，原样写进每条 `infra.notification.dispatch.im.v1` 的 `target_adapters`；默认 `dingtalk`。每个 IM 适配器都会收到每一条 dispatch，只处理点了自己名字的，所以这里漏掉的名字意味着那个适配器什么都不处理。只有 `integration/im-dingtalk` 一个 IM 适配器时保持默认；装第二个时加上它的名字。 |

## 契约索引

- `contracts/infra/notification/v1/notification.proto` — gRPC `infra.notification.v1.NotificationService`，给别的组件用：`BatchGetRecords`（查不到的 ID 直接省略）、`ListRecords`（游标分页；按类别、状态过滤，不按用户限制）、`GetPreferences` / `SetPreferences`（按 `sub`；`SetPreferences` 整份替换，天然幂等）。没有 `Notify` rpc：唯一入口是事件。Go 包是独立模块 `github.com/brickKit/infra-notification/gen/infra/notification`。
- `contracts/notification.openapi.yaml` — REST，前缀 `/infra/notification`，每条路由都有权限键：`GET /notifications`（`infra.notification.view`；只返回调用者自己的通知；`category`、`status`、`cursor`、`page_size`）、`GET /preferences`（`infra.notification.view`；调用者自己的偏好，补上默认值）、`PUT /preferences`（`infra.notification.preference.edit`；整份替换调用者的偏好：没提交或为 `null` 的 `global_channels` 跟随默认 `[IM]`，没提交或为 `null` 的类别跟随全局，空数组表示这一层全关；清空 critical 类别回 `400`）、`GET /admin/records`（`infra.notification.admin`；任何收件人，可选 `recipient_sub`，排障用）。
- `contracts/events/notification.events.json` — 经 Outbox 发布：`infra.notification.dispatch.im.v1`（每条 IM 记录一条；`record_id`、`attempt`、`target_adapters`、`recipient_phone`、`title`、`body`；事件版本等于 `attempt`，首次发送是 1，每重试一次加一）、`infra.notification.dispatch.email.v1`（同上，带 `recipient_email`；目前没有适配器消费它）、`infra.notification.sent.v1` 与 `infra.notification.failed.v1`（最终结果，供审计与监控）。消费：`infra.workflow.task.created.v1`（新待办 → 通知处理人）、`infra.iam.user.created.v1` / `infra.iam.user.updated.v1` / `infra.iam.user.disabled.v1`（联系方式快照；停用的用户什么都收不到，critical 类别也一样）、`integration.im.result.v1`（适配器的投递结果：先 `ACCEPTED`，再 `CONFIRMED` 带成功或可重试 / 不可重试的失败）。
- `assembly.yaml` — 本项目的元数据：上面三个权限键、菜单项、网关路由 `/infra/notification/**`、schema 与角色，以及 `notification_records.recipient_sub` 上的 `owner` 数据范围。

## 外壳声明

不是外壳。可以由 Go 外壳托管（在 BrickEnterprise 项目里是 `be/go-infra`），也可以独立运行；两种方式代码相同。
