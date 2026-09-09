# infra-notification · AI 助手导读

## 身份证

| 项 | 值 |
|---|---|
| 组件 ID | `infra/notification` |
| 仓库名 | `infra-notification` |
| 端口 | HTTP `8202` / gRPC `9202`（`registry/ports.tsv`，装配仓库根目录那份） |
| schema / role | `infra_notification` / `infra_notification_rw`（归档 schema `infra_notification_archive`，归档窗口全系统最短：3 个月） |
| 语言 / 框架 | Go：Gin + `database/sql` + `pgx/v5/stdlib` + `golang-migrate` |
| 合并部署时进 | 外壳三 `go-infra` |
| 装配角色 | `default` |
| 设计真相源 | 装配仓库 `docs/design/infra-notification.md`——本文件与它冲突时，以那份为准，回来改这里 |

## 边界

**归我：** 意图 → 消息的翻译（业务侧说"张三有一条待办"，我决定发几条消息、去哪些通道）、用户通道偏好、收件人联系方式快照、投递状态与重试、通知历史。设计书 §6.7 点名要解决的三件事：密钥管理混乱、重试逻辑重复、无法实现用户级通道偏好。

**不归我：**

| 什么 | 归谁 | 为什么 |
|---|---|---|
| 调外部 API 的那一下（钉钉/邮件/短信） | `integration-*` 适配器 | §6.9：适配器只监听事件 + 调外部 API + 发回调事件，外部平台的密钥、SDK、限流全在适配器里 |
| "什么情况下该通知" | 业务组件 / `infra-workflow` | 那是业务规则，我只在收到"请通知"的信号后才动 |
| 通知文案的业务措辞 | 触发方（放进事件 payload） | 我做的是模板渲染，不是决定"驳回时该说什么" |
| 用户身份、手机号的权威值 | `infra-iam-casdoor` → Casdoor | 我持的是快照，权威源在那边 |
| "这个错误值不值得重试" | 适配器（`integration-*`） | 只有适配器认得出平台特有的错误码，我只知道重试时机/次数 |

⚠️ **`data_scopes` 一维**：`owner`（`recipient_sub`，等值）——通知历史只该本人看得见，通知内容里会出现单据标题、金额这类摘要，这一维不是可选的。

## 契约面与事件

**gRPC `infra.notification.v1.NotificationService`：** `BatchGetRecords`（防 N+1）、`ListRecords`（组件间协议，不做数据权限过滤）、`GetPreferences`/`SetPreferences`（按 `sub` 读写）。

**REST：** `/infra/notification/**` 前缀。`GET /notifications`（我的通知历史）、`GET /preferences`/`PUT /preferences`（我的通道偏好）、`GET /admin/records`（排障，绕过 owner 维）。**没有 `Notify`**——本组件唯一入口是事件（§3.1：可被裁 + `infra-workflow` 被自己的铁律三禁止反向同步调用）。

**发布事件：** `infra.notification.dispatch.im.v1`/`.dispatch.email.v1`（核心，族级 subject，`target_adapters[]` 点名要哪些适配器处理，`attempt` 字段决定 `Event.Version`）、`.sent.v1`/`.failed.v1`（旁路）。

**消费事件：** `infra.workflow.task.created.v1`（本阶段唯一通知来源）、`infra.iam.user.{created,updated,disabled}.v1`（`user_contacts` 快照，按 `Event.Version` 单调更新）、`integration.im.result.v1`（族级，`adapter` 字段区分是谁发的，按 `phase` 推进状态机）。

## 依赖与「为什么不依赖某某」

`dependencies.components` 永远是空数组，本组件完全活在事件图上，零出边。

- **不依赖 `infra-iam-casdoor`**：联系方式走事件快照，不同步调它——对 `slot:iam` 建依赖边会当场废掉整个槽位机制。
- **不依赖任何 `channel:*` 适配器**：走 `dispatch.*` 事件。同步调用等于要求那个通道必须装，而 `channel:*` 族可装可不装是硬约束。
- **不依赖 `infra-workflow`**：我消费它的事件，方向单向，它不知道我存在。
- **`infra-workflow`/所有 `channel:*` 适配器都不依赖我**：谁都不建对我的依赖边——装了我，通知才会被路由；没装我，业务动作照样成功，只是没人收到通知。

## 这个组件特有的坑

| 不许 | 症状 | 出处 |
|---|---|---|
| 给"通知来源事件"的收件人提取逻辑加业务判断（比如"金额大于 X 就抄送主管"） | 没有立刻的症状，但下一次改需求就会有人想在这里加分支——那是触发方的业务规则，属于侵蚀设计计划 §3.1 的边界 | `backend/internal/consumer/consumer.go` 注释 |
| 重试重发 `dispatch.im.v1` 时用固定的 `Event.Version`（比如一直是 1） | **真实踩过的 bug**：`aggregate_id` 固定是 `record_id`，同一个 record 的多次派发共用同一个 `(subject, aggregate_id)`，`event_inbox` 按 version 严格递增去重，重试沿用旧 version 会被适配器的 `event_inbox` 当重复消息静默吞掉——症状是 `retry_count` 涨了、适配器那边什么都没收到，且没有任何报错 | `backend/internal/consumer/consumer.go` `publishIMDispatchTx` |
| 把用户偏好的"全局层"默认值设成 proto 里全部三个 `Channel` | 会给 EMAIL/SMS（本阶段没有适配器）建出永远停在 `PENDING`、没人会推进的死记录 | `backend/internal/repo/preferences.go` `defaultGlobalChannels` |
| 把"停用用户不该再收通知"的判断合并进 `ResolvePreferredChannelsTx` 的 critical 兜底逻辑 | 两者是不同层次的判断，合并了会让"停用用户还能通过 critical 兜底收到通知"这个真实的坑很难联想到是这里——必须分开判 | `backend/internal/repo/preferences.go` 注释、`backend/internal/consumer/consumer.go` |
| 给 `notification_records` 的 TEXT[] 列（`notification_preferences.channels`）直接 `Scan` 进 `*[]string` | `pgx/v5` 的 `database/sql` 通用接口不支持，报 `unsupported Scan`，真机测过 | `backend/internal/repo/pgarray.go` |
| 建 `command_idempotency` 表 | 全项目核对后确认这张表只用于"外部调用方提供 idempotency_key 的写命令 RPC"，本组件两个写入路径（`SetPreferences` 天然幂等、`CreateRecord` 事件驱动由 `event_inbox` 去重）都不需要它——已建过又在 `migrations/003` 删掉，别再加回来 | `migrations/003_drop_unused_command_idempotency.up.sql` |
| 给月分区/周分区维护任务加 `ALTER TABLE ... OWNER TO` | 多余的一步——真机验证过 `SET LOCAL ROLE` 之后 `CREATE TABLE` 的所有者就是当前角色本身，迁移建的初始分区靠 schema 的 `ALTER DEFAULT PRIVILEGES` 就有 DML 权限 | `backend/internal/partition/monthly.go` |

## 改代码前的自查

1. **我是不是在给某条消费的来源事件加业务判断？** 停下——只做"从 payload 里取收件人"这种直取，业务规则归触发方。
2. **我改的 dispatch 事件重发逻辑，`Event.Version` 是不是跟着 attempt 递增？** 停下并核对——这是本组件出现过的真实 bug。
3. **我是不是在给全局通道偏好默认值加 EMAIL/SMS？** 停下——没有适配器的通道不该是默认打开的。
4. **我是不是把"用户被停用"的判断塞进了 critical 兜底逻辑？** 停下——两层判断必须分开写。
5. **我是不是在给某个 TEXT[] 列直接 Scan 进 `*[]string`？** 停下——pgx 不支持，要走 `pgarray.go` 的手解。
6. **我是不是在给某个写路径加 `command_idempotency`？** 停下——先确认这个写路径是不是"外部调用方提供 idempotency_key"，本组件目前没有这种场景。
7. **我是不是在给分区维护任务加 `ALTER TABLE ... OWNER TO`？** 停下——不需要，见上表最后一条。
