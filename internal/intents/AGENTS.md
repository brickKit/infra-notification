# intents：收意图

收部署配置的发送方的通知意图（`docs/design.md` 的 NT01、NT02、NT04；线协议 P18.7–P18.9）。

## 负责与不负责

- 负责：表 `intent` 的写入、`collapse`、`rejected_intent`；订阅（`Subs`：配置里每个发送方的流）；`Admit`（纯函数：拒收的原因按 P18.8 的顺序、重复、合并键的旧序号、取代与投递）；`OnEvent`（形状核对时把类别换成目录里的，使“不在目录”在形状之后单独报出；锁住合并键的行——第一次先插占位行；按 `Admit` 的答案：拒收写一行与一条安全审计、重复不动、旧序号存成 `superseded`、取代时记新序号并撤回同键的待投意图与站内消息、撤回意图直接 `done`、投递意图存成 `pending` 并排一轮投递 `JobDeliverNow`）；被拒意图的列表（`Rejected`）。
- 不负责：投递（`delivery`）；配置的格式（`sources`）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `admit.go` | `Arrival`、`Admission`、`Admit` |
| `admit_test.go` | 单元测试：`Admit` 对线协议的向量 `notification-intent`（admit） |
| `intents.go` | `JobDeliverNow`、`Subs`、`OnEvent`、`shapeOK`、`supersede`、`insert`、`reject`、`Rejected` |

## 依赖与被依赖

- 依赖：`core`、`sources`；SDK 的 `notify`（契约与核对）。
- 被依赖：入口（订阅、路由、作业名）。

## 测试

`itest/delivery_integration_test.go`（重复、合并与撤回、乱序）、`itest/senders_integration_test.go`（拒收的四种原因、没配的发送方、安全审计）。
