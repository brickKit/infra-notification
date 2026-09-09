# infra-notification · 统一通知中心

接收业务组件的"通知意图"，按用户通道偏好路由给下层已装配的 `channel:*` 适配器——密钥管理、重试逻辑、通道偏好三件事统一在这里做（设计书 §6.7）。**没有 `Notify` 这类同步接口**：谁要通知，谁发自己的领域事件，本组件消费；本组件发 `dispatch.*` 事件，`channel:*` 适配器消费并回发结果事件。

## 它能做什么

- `infra.notification.v1.NotificationService`（gRPC，组件间协议）：`BatchGetRecords`（防 N+1）、`ListRecords`（组件间协议，不做数据权限过滤）、`GetPreferences`/`SetPreferences`（按 `sub` 读写任意用户的偏好，供其它组件的排障工具用）
- REST（人类操作，`/infra/notification/**`）：
  - `GET /notifications`：我的通知历史，`recipient_sub` 强制等于调用者自己
  - `GET /preferences` / `PUT /preferences`：我的通道偏好（两层模型，见下）
  - `GET /admin/records`：排障视图，绕过 owner 维
- 消费 `infra.workflow.task.created.v1`（本阶段唯一的通知来源）、`infra.iam.user.{created,updated,disabled}.v1`（维护收件人联系方式快照）、`integration.im.result.v1`（族级投递结果，推进状态机）
- 后台循环：Outbox 推送、周分区维护（`event_outbox`/`event_inbox`）、月分区维护（`notification_records`，本组件唯一无限增长的表）

⚠️ **没有"发通知"的接口**——本组件可被裁，任何同步调用方都要建依赖边，而"通知发不出去"绝不该让业务动作失败；`infra-workflow` 又被自己的铁律三禁止反向同步调用。走事件天然没有这个问题。

## 需要哪些基础资源

| 资源 | 形态 | 为什么需要 | 怎么起 |
|---|---|---|---|
| PostgreSQL 16 | **A**（`kind: database`） | `notification_records`（月分区）/`notification_preferences`/`user_contacts` 独占 schema `infra_notification` | 装配仓库根目录 `make up` |
| NATS 2.10 | **A**（`kind: mq`） | 消费三类事件、发 `dispatch.*`/`sent.v1`/`failed.v1` | 同上 |

⚠️ **零强依赖零弱依赖，零出边**——完全活在事件图上（设计计划 §5、§6）。联系方式走 `infra-iam-casdoor` 的事件快照，不建依赖边（对 `slot:iam` 建依赖边会当场废掉整个槽位机制）；不知道、也不该知道谁在消费 `dispatch.*`（`channel:*` 族可装可不装）。

## 怎么起来

```bash
# 装配仓库根目录
make up
cd components/infra/notification
go build -o build/migrate ./backend/cmd/migrate
PG_SCHEMA=infra_notification DATABASE_HOST=localhost DATABASE_PORT=5432 \
  DATABASE_USER=postgres DATABASE_PASSWORD=<.env 里的 POSTGRES_PASSWORD> DATABASE_NAME=brickkit_db \
  ./build/migrate up
go run ./backend/cmd/server
```

或者用平台：`brickkit up`。

## 怎么用

```bash
# 人查自己的通知历史
curl -H 'Authorization: Bearer <应用 token>' http://localhost:8202/infra/notification/notifications

# 人改自己的通道偏好——workflow_task 是 critical 类别，清空会被拒绝
curl -X PUT -H 'Authorization: Bearer <应用 token>' -H 'Content-Type: application/json' \
  -d '{"global_channels":["IM"],"category_channels":{"workflow_task":["IM"]}}' \
  http://localhost:8202/infra/notification/preferences

# 组件间协议：查某几条通知的状态（排障/联调用）
grpcurl -plaintext -d '{"record_ids":["1","2"]}' \
  localhost:9202 infra.notification.v1.NotificationService/BatchGetRecords
```

真实触发一条通知：让 `infra-workflow` 真的 `CreateTask` 一次，本组件会消费 `infra.workflow.task.created.v1`，按收件人的通道偏好建 `notification_records` 并发 `infra.notification.dispatch.im.v1`。

## 配置项

| 配置键 | 默认值 | 说明 |
|---|---|---|
| `pgSchema` | `infra_notification` | 本组件的 PG schema |
| `otelBaseUrl` | `""` | 空 = Blackhole Exporter，零成本 |
| `iamJwksUrl` | `""` | JWT 本地验签的公钥来源 |
| `authzBundleUrl` | `""` | 权限判定的 bundle 轮询地址 |
| `imTargetAdapters` | `dingtalk` | 发 `dispatch.im.v1` 时点名哪些 `channel:im` 适配器处理，逗号分隔；装第二个 IM 适配器时改这一个字符串，不用改代码 |

### 两层偏好模型 + critical 类别不可关闭

全局通道开关（`category=''`，天花板）+ 分类通道开关（具体类别，天花板的子集，未设置就跟随全局）。`workflow_task` 是本阶段唯一的 critical 类别：用户能选走 IM 还是别的通道，**但不能选择一个都不走**——`SetPreferences` 对 critical 类别显式提交空数组会被拒绝，且解析逻辑（`ResolvePreferredChannelsTx`）在正常情况下永远不会为它解出空集（唯一的例外见下一条）。

⚠️ **停用用户覆盖 critical 兜底**：`infra.iam.user.disabled.v1` 消费后，即使类别是 critical，也不会再给这个人发通知（记一条 `SUPPRESSED`，供排障可见）——这条判断在 critical 兜底逻辑之外单独判一次，不与其合并。

### 状态机：`ACCEPTED` 中间态不能省

`PENDING → ACCEPTED（已受理待确认）→ SENT / FAILED_PERMANENT`，`RETRYING` 是活跃态。查证钉钉文档确认 `asyncsend_v2` 只返回"受理了"的 `task_id`，真实投递结果要另查——从 `PENDING` 直接跳 `SENT` 是把"已提交"当成"已送达"。重试立即重发（不做退避），达到 `maxRetries=3` 后转 `FAILED_PERMANENT`。

⚠️ **重试重发时事件 `Version` 必须递增**：`dispatch.im.v1` 的 `aggregate_id` 固定是 `record_id`，同一个 `record_id` 的初次派发与每次重试派发共用同一个 subject+aggregate_id，`event_inbox` 按 version 严格递增去重——这是实现时真的踩出来过的 bug，见 `backend/internal/consumer/consumer.go` 的 `publishIMDispatchTx` 注释。

## 参考实现

| 项目 | 看的模块 | 借鉴了什么 | 许可证 | 用法 |
|---|---|---|---|---|
| Novu | Workflow/Step 模型、subscriber 与 channel preference 的数据结构 | 用户级通道偏好的建模，简化成 `sub × 类别 × 通道`；critical 类别不可关闭的概念 | MIT | 借鉴逻辑 |
| Novu | provider 抽象层 | 反面教材：不把 provider 做成内部插件，拆成独立的 `integration-*` 组件——密钥要跟着适配器走 | MIT | 借鉴逻辑 |
| ERPNext | `Notification` doctype + `Email Queue` | 投递状态机与重试计数的落库方式 | GPL-3 | 借鉴逻辑 |

## 边界与禁令

- **没有 `Notify` 同步接口**——唯一入口是事件，加一条同步接口就等于要求本组件必须装
- **不持有外部平台的密钥**——那是各 `integration-*` 适配器的事，本组件甚至不知道装了哪几个
- **零事件消费之外的业务规则**——每多消费一条来源事件只做"从 payload 里取收件人"这种直取，不许出现"金额大于 X 就抄送主管"这类判断（那是触发方的业务规则）
- **`retryable` 由适配器判定，重试时机/次数由本组件决定**——两边分工不能混
- **不做手机号/邮箱的权威校验**——`user_contacts` 是 `infra-iam-casdoor` 事件的快照，权威源在 Casdoor
- **`notification_records` 是唯一按月分区的表**，归档窗口全系统最短（3 个月）——它是过程数据，价值随时间掉得比业务单据快
