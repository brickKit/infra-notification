[English](design.md) · [中文](design.zh.md)

# infra/notification 设计

只写结论，给要改本组件设计的人看。怎么用：`BRICKKIT.zh.md`；代码怎么组织：`AGENTS.zh.md`。

## 边界

本组件把"意图"翻译成"消息"：业务事件说的是"这个人有一条待办"，由本组件决定这变成几条消息、走哪些通道。它拥有用户的通道偏好、收件人联系方式的快照、每条消息的投递状态与重试，以及通知历史。重试逻辑只在这里写一遍；外部平台的密钥离它远远的。

| 不在这里 | 归谁 | 为什么 |
|---|---|---|
| 调钉钉、邮件、短信的那一下 | `integration/*` 通道适配器 | 平台密钥、SDK、限流跟着适配器走；一个持有所有平台密钥的通知中心，正是这种拆法要避免的问题 |
| 什么事值得通知 | 发事件的组件（目前是 `infra/workflow`） | 那是业务规则；本组件只在被告知之后才动 |
| 业务场景的措辞 | 发事件的组件，放在事件 payload 里 | 本组件用 payload 拼消息正文（目前是 `您有一条新的待办事项待处理：<title>`），不决定"驳回时该说什么" |
| 用户是谁、权威的手机号与邮箱 | `infra/iam-casdoor`（背后是 Casdoor） | 本组件只持身份事件的快照 |
| 某个平台错误值不值得重试 | 适配器 | 只有适配器认得平台的错误码（"不是本组织成员"是永久失败） |
| 通知列表的展示 | 前端 | 本组件提供 REST 读接口 |

每多消费一条来源事件，只多一小段"从 payload 里取收件人"，不多别的：不许有"金额大于 X 就抄送主管"。消费的来源事件超过五条时，该走的一步是通用意图事件（`infra.notification.requested.v1`，由发起方给出收件人与模板），而不是第六个 handler；在那之前，通用事件会逼所有发起方都改，换不来任何好处。

到达策略：解析出的每个通道并发发送，用户可以关掉某个通道。"IM 失败了才发短信""每个用户只选一个主通道"也都是合理的策略；它们的差别只是本组件内部几十行路由逻辑，事件完全相同，所以是本组件的一个 Fork 点，而不是一组可替换的独立组件（槽位族）。

## 拥有的数据

| 表 | 分区 | 说明 |
|---|---|---|
| `notification_records` | 按月，`created_at` | 每条消息一行：收件人、类别、通道、标题、正文、状态、`external_task_id`、`retry_count`、`last_error`、来源组件 / 聚合 / ID。唯一会无限增长的表：一条待办可能变成好几条消息（通道 × 提醒次数） |
| `notification_preferences` | 不分区 | `(sub, category)` 唯一；`category = ''` 是全局层，类别名是分类层；`channels` 是 `text[]`。按用户数有界 |
| `user_contacts` | 不分区 | `sub` → 手机号、邮箱、显示名、`disabled`、`version`。身份快照，按用户数有界；用户停用后保留，历史记录才能说出发给过谁 |
| `event_outbox`、`event_inbox` | 按周，`created_at` | Outbox 推送从这里发布，Inbox 给消费的事件去重 |

状态：`PENDING`（记录已建，适配器还没确认 dispatch）→ `ACCEPTED`（平台受理了消息，还没送达）→ `SENT`，或者 `RETRYING` → … → `FAILED_PERMANENT`。`SUPPRESSED` 是另一条终态，表示这条消息谁都不该收到（目前是收件人已停用）。终态：`SENT`、`FAILED_PERMANENT`、`SUPPRESSED`。

`ACCEPTED` 不能省：钉钉工作通知接口返回的任务 ID 只代表"受理了"，真实结果要之后再查。从 `PENDING` 直接跳 `SENT` 等于在只知道"已提交"的地方记了"已送达"，这是最难查的一类投诉。

没有手机号的收件人得到一条 `FAILED_PERMANENT` 记录并写明原因，而不是 `SUPPRESSED`：`SUPPRESSED` 的意思是"因偏好或用户状态而不发"，缺手机号是数据缺口。记录留着，排障视图里看得到。

偏好分两层，借鉴 Novu：

- **全局层**是天花板（"我不要短信"）；没设置过就是默认 `[IM]`。EMAIL 与 SMS 不在默认里，因为目前没有适配器消费它们：默认打开只会造出永远停在 `PENDING` 的记录。
- **分类层**在其中收窄（"审批走 IM，日报走邮件"）；没设置过就"跟随全局"，设置过就与全局层取交集。分类层不能突破全局层的"关"。
- **critical 类别**关不掉：用户选的是走哪个通道，不是要不要收。目前 `workflow_task` 是 critical，因为审批通知要是能被静音，业务链路就断了，而系统看起来一切正常。`PUT /preferences` 拒绝给 critical 类别提交空数组（`400`）；critical 类别解析出空集时兜底成默认值。
- **停用的用户**什么都收不到，critical 也一样。这是消费者里在偏好解析之后单独做的判断，绝不与 critical 兜底合并。
- `PUT /preferences` 整份替换。缺字段或写 `null` 表示"没提交"：全局层于是跟随默认，分类跟随全局层。空数组表示"这一层全关"。（`channels` 列是 `NOT NULL`；缺值绝不会写成 `NULL`。）

## 契约面

gRPC `infra.notification.v1.NotificationService`，给别的组件用：

| rpc | 类型 | 说明 |
|---|---|---|
| `BatchGetRecords` | 读 | 批量读；未知或格式不对的 ID 直接省略 |
| `ListRecords` | 读 | 游标分页（最后一条记录的 ID），过滤 `category`、`status`；不按用户限制：调用方是组件，gRPC 上不带用户 token |
| `GetPreferences` | 读 | 按 `sub`，补上默认值 |
| `SetPreferences` | 写 | 按 `sub`，整份替换，天然幂等（没有 `idempotency_key`）；经 gRPC 提交空的全局列表表示"全局全关" |

REST，前缀 `/infra/notification`，每条路由都有权限键：

| 路由 | 权限键 | 说明 |
|---|---|---|
| `GET /notifications` | `infra.notification.view` | 调用者自己的历史；`recipient_sub` 强制等于调用者 |
| `GET /preferences` | `infra.notification.view` | 调用者自己的偏好。读自己的偏好与读自己的通知是同一档：偏好页面一打开就要读，读不应该要编辑权限 |
| `PUT /preferences` | `infra.notification.preference.edit` | 替换调用者的偏好（规则见上） |
| `GET /admin/records` | `infra.notification.admin` | 排障：任何收件人，可选 `recipient_sub`；例如停在 `ACCEPTED`（`status=ACCEPTED`）的记录说明某个适配器的结果回查断了 |

没有 `Notify` rpc，也没有 REST 的"发送"：唯一入口是事件。本组件默认安装但可以不装，同步调用会逼每个调用方依赖它，而通知发不出去绝不该让业务动作失败；目前唯一的来源 `infra/workflow` 也不允许反向同步调用对它作出反应的组件。只有一个入口也意味着只有一套幂等、一条重试路径。

没有超时写入的状态端点，也没有 `command_idempotency` 表：写入只有 `SetPreferences`（天然幂等）和建记录，后者由事件驱动、由 Inbox 去重。

## 事件

发布，全部经 Outbox，与记录的变化在同一个事务里：

| Subject | 何时 | Payload |
|---|---|---|
| `infra.notification.dispatch.im.v1` | 建出一条 IM 记录时，以及每次重试 | `record_id`、`attempt`、`target_adapters`、`recipient_phone`、`title`、`body` |
| `infra.notification.dispatch.email.v1` | 建出一条 EMAIL 记录时 | `record_id`、`attempt`、`target_adapters`、`recipient_email`、`title`、`body`（目前没有适配器消费） |
| `infra.notification.sent.v1` | 适配器确认成功 | `record_id`、`channel` |
| `infra.notification.failed.v1` | 适配器报告永久失败，或重试耗尽 | `record_id`、`channel`、`error` |

- **每个通道族一个 subject，用 `target_adapters` 点名。** 每个 IM 适配器（钉钉、企业微信、飞书……）都订阅 `dispatch.im.v1`；NATS 核心把它投给所有订阅者，每个适配器只处理点了自己名字的 dispatch。加一个 IM 适配器改的是 `IM_TARGET_ADAPTERS`，不是本组件；每个适配器一个 subject 会把装配期的知识搬进运行时。
- **dispatch 的版本就是 attempt。** 聚合是 `record_id`，所以首次发送与每次重试共用 subject 与聚合；适配器的 Inbox 只接受递增的版本，所以 `Version = attempt`（1，然后 2、3、4）。沿用旧版本的重发会被静默丢掉：`retry_count` 涨了，什么都没送到。
- **重试**：收到 `CONFIRMED` 失败且 `retryable = true` 时，`retry_count` 加一并立即重发 dispatch（真实的限流数据要求之前不做退避）；第三次重试之后再失败，记录转 `FAILED_PERMANENT`。`retryable = false` 立即终止。

消费：

| Subject | 来自 | 做什么 | 重复与乱序 |
|---|---|---|---|
| `infra.workflow.task.created.v1` | `infra/workflow` | 解析处理人的通道；每个通道一条记录；每条 IM / EMAIL 记录一条 dispatch；处理人已停用时只落一条 `SUPPRESSED` | Inbox：任务 ID 与版本 |
| `infra.iam.user.created.v1`、`infra.iam.user.updated.v1` | `infra/iam-casdoor` | upsert 手机号、邮箱、显示名；从不碰 `disabled`，一次资料更新不会悄悄把人重新启用 | 只在事件版本大于已存版本时生效 |
| `infra.iam.user.disabled.v1` | `infra/iam-casdoor` | 置 `disabled`，需要时新建这一行（停用可能比创建先到） | 同样的版本规则 |
| `integration.im.result.v1` | 任一 IM 适配器 | `ACCEPTED` 记下适配器的任务 ID；`CONFIRMED` 转 `SENT`、`RETRYING`（并重发）或 `FAILED_PERMANENT`；已结束的记录收到迟到的结果则忽略 | Inbox：按 `record_id`，适配器的版本 `attempt × 10 + phase` |

结果 subject 也是每族一个，适配器名放在 payload 里，理由同 dispatch：第二个 IM 适配器不需要改这里。`user.disabled.v1` 必须消费：给已经离职的人发审批通知，是本组件最显眼的失败。

消费走普通的 NATS 订阅：本组件停机期间发布的事件，之后不会再投给它。持久投递是平台层的决定（SDK 里的 JetStream），不是本组件单独能做的。

## 依赖

无，设计如此。预期中有、实际没有的：

| 不是依赖 | 为什么 |
|---|---|
| `infra/iam-casdoor` | 联系方式来自它的事件；对身份槽位建同步边，就没法换成别的 IAM 了 |
| `integration/*` 适配器 | 经 dispatch 事件到达；通道适配器可装可不装，一次调用就会让它们变成必装 |
| `infra/workflow` | 消费它的事件；方向单向，它不知道本组件存在 |
| `infra/authz` | 权限 bundle 从 `AUTHZ_BUNDLE_URL` 轮询：是配置，不是依赖边 |
| NATS | 经 `NATS_URL` 访问的基础资源，不是组件 |

也没有组件依赖本组件：装了，通知会被路由；没装，每个业务动作照样成功，只是没人收到通知。

## 在同步调用图里的位置

不在里面：没有出边也没有入边的同步调用，所以永远不会成环，也与"CRM 与 ERP 之间没有同步边"无关。它只活在事件图上。形态最接近 `erp/finance`（只消费、不调人的事件汇）；区别是财务把消费到的东西记账，本组件把它发出去。

## 分区与归档

| 数据 | 热数据 | 归档条件 | 去哪 |
|---|---|---|---|
| `notification_records` | 最近 3 个月 | 早于此的整月分区，并且只归终态的行 | `infra_notification_archive` |
| `notification_preferences` | 永远 | 不归档 | — |
| `user_contacts` | 永远 | 不归档（历史要能说出收件人是谁） | — |
| `event_outbox`、`event_inbox` | 周分区 | 按 Outbox / Inbox 的约定 | — |

窗口是项目里最短的（3 个月，别的是 12 个月）：通知是过程数据，价值掉得比业务单据快，增长又最快。`RETRYING` 的行不会因为老就被归档。组件自己建月分区（当月加未来三个月）和周分区（未来四周）；分区名必须与迁移建的一致（`<表名>_YYYY_MM_01`），否则后台循环会去建一个范围重叠的分区。归档本身还没有实现（见未决问题）。

## 数据范围

一维：`notification_records.recipient_sub` 上的 `owner`，`equals`。通知历史只有收件人本人看得见，因为通知里带着单据标题与金额：看得到别人的通知，就绕过了发起它的组件的数据范围。REST 列表不管查询参数怎么写，都把 `recipient_sub` 强制成调用者的 `sub`；没有"主管看下属的通知"。`GET /admin/records` 按设计绕过范围，要 `infra.notification.admin`。gRPC 是组件之间的调用，不按范围过滤。

## 参考实现

| 项目 | 版本 | 看的模块 | 借鉴了什么 | 许可证 | 用法 |
|---|---|---|---|---|---|
| Novu | 未记录 | workflow / step 模型、subscriber 与通道偏好 | 用户级通道偏好，简化成 `sub × 类别 × 通道`；全局压过分类；用户关不掉的 critical 通知 | MIT | 借鉴逻辑 |
| Novu | 未记录 | provider 层 | 反面：provider 做成内部插件；这里拆成独立组件，因为密钥跟着适配器走 | MIT | 借鉴逻辑 |
| ERPNext | 未记录 | `Notification` doctype、`Email Queue` | 投递状态机与每条消息的重试计数落库 | GPL-3 | 借鉴逻辑 |
| 钉钉开放平台 | — | 工作通知 | 受理与送达的区别（先返回任务 ID，再查结果）；严格的频控，又一个把重试集中在一处的理由 | 闭源 | 借鉴实际应用 |

**刻意避开的**：通知中心自己持有各平台的密钥与 SDK；业务组件同步调用通知接口；把通知条件写成可配置表达式（ERPNext 的 `condition`）：运行时字符串，AI 读不懂也测不了，而且"什么时候通知"归发起方。刻意没参考的：消息中间件的重试与死信设计；那是传输层的重试（SDK 的 Outbox / Inbox），本组件的重试是业务语义的（"不在组织里"不能重试）。

## 未决问题

| 问题 | 目前的答案 |
|---|---|
| `notification_records` 3 个月后的归档 | 设计好了（整月、只归终态的行），还没实现；实现之前月分区会一直累积 |
| 重试参数 | 三次立即重试；退避曲线等适配器给出真实的限流数据。适配器分辨不了的错误一律算可重试，但计入次数上限 |
| 没有手机号的收件人（例如社交登录注册的） | `FAILED_PERMANENT` 并写明原因，`GET /admin/records` 里看得到；要在待办本身上显示"未能通知"，得由 `infra/workflow` 来读，暂无计划 |
| 停在 `ACCEPTED` 的记录 | 用 `GET /admin/records?status=ACCEPTED` 能查出来；还没有时长阈值或告警 |
| 通知列表的已读 / 未读 | 暂不做；前端要未读角标时再加 `GET /notifications/unread-count` 与 `POST /notifications/{id}/read`（权限键 `infra.notification.view`，加 `read_at` 列） |
| 系统告警走不走本组件 | 暂不；告警的收件人是运维而不是业务用户，偏好模型未必适用 |
| EMAIL 与 SMS | subject 已占位（`dispatch.email.v1`）；重试与结果事件目前只有 IM，有了适配器再按通道扩展 |
