# infra/notification

## 组件定位

通知：收下部署配置的发送方发出的通知意图（线协议第 18 章的共享意图契约），按类别与每个人的偏好投递：本组件自己的站内消息，与部署配置的外部渠道（渠道成员，线协议第 20 章；第一个是 `integration/im-dingtalk`）。
- **认收**：只收配置里的发送方的意图，发送方以意图所在的流为准（消息系统按服务身份限制谁能在哪个流上发布）；不合契约、发送方或来源不是流的属主、类别不在目录里、到期不晚于发生的，拒收并写安全审计（重投不重复记），管理员能列出来。同一意图再到不再投。
- **合并与撤回**：同一发送方、同一合并键的意图，序号大的取代序号小的（已写的站内消息标为已撤回），迟到的旧序号作废；撤回意图只做取代（例：审批任务决定后撤回待审批的消息）。
- **收件人**：主体，或岗位（投递时按员工投影展开为当时的持有人，每个意图只展开一次；投影不完整时等，不按旧数据投；部署没有 `mdm/employee` 时岗位展开不了）。
- **类别与偏好**：审批待办、到期提醒、安全提醒、法律通知、对外通信、营销；安全提醒与法律通知必须送达：偏好关不掉，到期也照写站内消息；营销默认关，其余默认开。
- **我的站内消息**：只看自己的，新的在前，未读数，标为已读、全部已读。
- **投递结果**：每个投递意图投完（每个收件人都有结果）发一条事件（主体：已写入、按偏好不投、过期；岗位：已展开、展开不了、过期）；被更新的序号取代的意图不发；过期的按类别计数，必须送达的类别有岗位过期或展开不了时写告警级日志。

通知不授予权限：消息的链接打开时，由链接指向的组件按读者当时的权限再检查。

**3.0.9**：发送方可以要求只写站内（`in_app_only`，例：流程内核告诉点按钮的人“按钮没生效”，不再投回钉钉）；外部投递的作业一轮至多 20 行，同一行不会被两轮重复交出；渠道不可达时被跳过的行不算一次尝试。

**3.0.6**：外部渠道（见部署前准备的 `NOTIFICATION_CHANNELS`）与交互按钮的动作授权（只交给 `action_channels` 里的渠道）。

**3.0.0**（第一个重建的版本，契约 `contract/v3.0.0-alpha.1`；3.0.2 起 alpha.2，投递结果加“已展开”）：上述全部；外部渠道（IM、邮件、短信）、免打扰与摘要合并、伙伴联系人的收件人、交互按钮以后做。

## 部署前准备

- 数据库：PostgreSQL 16 及以上；schema `infra_notification`，属主角色 `infra_notification_owner`（只供迁移）与运行角色 `infra_notification_rw`（只有读写数据的权限），名字来自装配项目的注册表。三者由部署方在第一次启动前建好；表由本组件的迁移建，迁移里不建角色、不授权。
- 消息系统（`BUS_URL`）与实现实例（`IMPLEMENTATION_INSTANCE`）：本组件在流 `infra.notification` 上发投递结果；按 `NOTIFICATION_INTENT_SOURCES` 订阅每个发送方的流（组件 `erp/purchase` 的流 `erp.purchase` 上的 `erp.purchase_notification.intent.v1`）。
- **`NOTIFICATION_INTENT_SOURCES`**：逗号分隔的发送方组件 ID，例 `erp/purchase,erp/payment`（用 SDK 流程内核的组件发审批待办的意图）。格式不对、重复，成员不启动；不配时什么也不收。
- **`NOTIFICATION_CHANNELS`**：外部渠道，逗号分隔的“渠道名=渠道成员”，例 `dingtalk=integration/im-dingtalk`。渠道成员要在部署里（本组件把它声明为可选依赖，地址由平台注入），并把本组件的投递操作（钉钉是 `integration.im_dingtalk.system.deliver`）授予本组件。审批待办、到期提醒、安全提醒投到站内与全部外部渠道；外部渠道可以按“类别 × 渠道”关掉；不配时只有站内。
- 可选 `mdm/employee`：岗位类收件人按它的员工投影展开（只经事件与它的水位来源，不在请求里调它）；没有时岗位类收件人展开不了，发给主体的照常。
- 授权：`infra.notification.message.read`、`infra.notification.message.mark`、`infra.notification.preference.read`、`infra.notification.preference.manage` 授予所有要用站内消息的人（每个人只看得到、改得到自己的）；`infra.notification.rejection.read` 给排障的管理员。
- 部署的服务证书（`SERVICE_TLS_*`）；身份与授权（`IAM_*`、`AUTHZ_GRPC_URL`、`DEPLOYMENT_ID`）；审计（`AUDIT_GRPC_URL`）。
- 低负载、后台为主：适合外壳（`be/go-infra`）。

## 依赖说明

- 必需：数据库、消息系统、身份成员、授权成员。
- 可选：`mdm/employee`（岗位展开）、审计组件、渠道成员 `integration/im-dingtalk`（钉钉；`NOTIFICATION_CHANNELS` 配了它才用）。
- 只经事件：配置里的发送方（不声明依赖；没装的发送方的流是空的，订阅只是等着）。
- 投递的轮次：收下一个意图时立刻一轮，员工投影变化而有意图在等时一轮，此外每分钟一轮；等投影的岗位与已到期的意图最迟一分钟收尾。

## 配置指南

全部键见 `component.yaml` 的 `configSchema`（由 SDK 生成，测试核对）。

| 键 | 说明 |
| --- | --- |
| `NOTIFICATION_INTENT_SOURCES` | 收谁的意图（见部署前准备） |
| `NOTIFICATION_CHANNELS` | 外部渠道（见部署前准备） |
| `DEPLOYMENT_ID`、`IAM_ISSUER`、`IAM_JWKS_URL`、`AUTHZ_GRPC_URL`、`SERVICE_TLS_CERT_FILE`、`SERVICE_TLS_KEY_FILE`、`SERVICE_TLS_CA_FILE`（必填） | 受保护的用户面与 mTLS 的系统面要用；部署身份、身份与授权的地址在外壳里是进程级的键 |
| `PG_HOST`、`PG_DATABASE`、`PG_USER`、`PG_PASSWORD`、`PG_SCHEMA`（必填） | 自己的数据库，密码是密钥；迁移另用 `PG_OWNER_USER`、`PG_OWNER_PASSWORD` |
| `BUS_URL`（必填，密钥）、`IMPLEMENTATION_INSTANCE`（必填） | 消息系统；实现实例是安装时生成的随机身份 |
| `AUDIT_GRPC_URL` | 审计组件的系统面；缺时审计记录留在本地，按协议 03 处理 |

## 契约索引

- **系统面**：没有本组件自己的服务（补发与整套恢复的核对是 SDK 的标准服务：`infra.notification.system.resend`、`infra.notification.system.recover`）。
- **用户面**：`contract/openapi.yaml`。`GET /api/notification/messages`（我的站内消息与未读数）要 `infra.notification.message.read`；`POST /api/notification/messages/{id}/read`、`POST /api/notification/messages/read-all` 要 `infra.notification.message.mark`；`GET /api/notification/preferences` 要 `infra.notification.preference.read`；`PUT /api/notification/preferences/{category}/{channel}` 要 `infra.notification.preference.manage`；`GET /api/notification/rejected-intents` 要 `infra.notification.rejection.read`。资源没有维度。
- **事件**：发布 `infra.notification_delivery.recorded.v1`（`contract/events/`）。订阅的载荷是线协议的共享意图契约 `schemas/notification/intent.v1`；`component.yaml` 的 `events.subscribes` 列出已知发送方的前缀（`erp.purchase_notification.*`、`erp.payment_notification.*`），实际订阅由 `NOTIFICATION_INTENT_SOURCES` 决定；员工的三种事件（岗位展开）。

## 外壳声明

不是外壳。可以被 Go 的基础外壳（`be/go-infra`）承载。
