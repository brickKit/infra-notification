# infra/notification 的契约

组件 `infra/notification`（通知）对外发布的契约，随组件的版本发布：

| 文件 | 内容 |
| --- | --- |
| `openapi.yaml` | 用户面：我的站内消息（未读数、标为已读、全部已读）、我的偏好、被拒的意图；每个路由的操作（`x-operation`） |
| `events/infra.notification_delivery.recorded.v1.schema.json` | 投递结果：一个意图的每个收件人在每个渠道的结果（增量事实） |

本组件不拥有意图的契约：通知意图的 schema 在线协议 `be-protocol` 的 `schemas/notification/intent.v1.schema.json`（发送方经 SDK 的 `notify` 按它发出），本组件按它消费。系统面没有本组件自己的服务。
