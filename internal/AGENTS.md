# internal/：业务模块

通知组件的业务，每个模块一个包。只给本仓库引用（Go 的 `internal`）：别的组件编译期就引用不了（决定 0101）。

## 模块

| 包 | 一句话 | 依赖（本组件内） |
| --- | --- | --- |
| `intents` | 收意图：发布者核对、形状、去重、合并键的序号与取代、被拒的意图 | `core`、`sources` |
| `delivery` | 投递：岗位展开、路由、站内消息、结果、到期、事件、清理 | `core` |
| `inbox` | 用户面：我的站内消息与已读 | `core` |
| `preferences` | 用户面：我的偏好 | `core` |
| `sources` | 部署的发送方配置（纯函数） | `core` |
| `core`（共用） | 名字、资源与操作、类别目录、参数、错误与校验、游标、`Base` 与目录接口 | — |

分层（每个包只引用比它低的层，没有环）：

| 层 | 包 |
| --- | --- |
| 3 | `intents`、`delivery`、`inbox`、`preferences` |
| 2 | `sources` |
| 1 | `core` |

核对：`go list -f '{{.Imports}}' ./internal/<包>`。

## 共同的写法

- **每个模块的函数先收 `m *core.Base`**（运行时、配置的发送方、岗位的目录），入口包的 `Module` 持有它、登记时适配。
- **规则的判断是纯函数**：认收（`intents.Admit`）与路由（`delivery.Route`）对线协议的向量测；写库的函数按它们的答案行事。
- **列表按游标分页**（`core.Cursor`、`core.FromCursor`），不用偏移量。

## 测试

`intents`、`delivery`、`sources` 有单元测试（前两个对线协议的向量）；其余由 `itest/` 的流程测试覆盖。
