# delivery：投递

把 `pending` 的意图投出去（`docs/design.md` 的 NT05–NT07、NT10–NT12）。

## 负责与不负责

- 负责：表 `message` 的写入、`delivery`；`Route`（纯函数：到期、必须送达、偏好与默认）；`Deliver`（一轮：逐批 `FOR UPDATE SKIP LOCKED` 认领，一轮里每个意图只试一次；岗位按目录展开、投影不完整时等、空集或没有员工组件记 `unresolvable`；同一主体只投一次；每次投递在 `delivery` 唯一；全部收件人有结果时意图 `done` 并发投递结果事件；到期时没结果的记 `expired`）；`EmployeesChanged`（员工投影变化而有意图在等时排一轮）；计数（`Counters`：过期的投递按类别）与告警（必须送达的类别过期或展开不了：告警级日志）；清理（`Purge`：意图 90 天、消息 180 天、合并键 400 天、被拒 90 天）。
- 不负责：收意图（`intents`）；读给用户（`inbox`）；偏好的修改（`preferences`）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `route.go` | `Route` |
| `route_test.go` | 单元测试：`Route` 对线协议的向量 `notification-intent`（route） |
| `delivery.go` | `JobDeliver`、`JobPurge`、`Counters`、`Deliver`、`claim`、`deliver`、`expand`、`deliverTo`、`record`、`publish`、`Purge`、`EmployeesChanged` |

## 依赖与被依赖

- 依赖：`core`；SDK 的 `notify`（收件人的形状）、`flow`（`Enqueuer`）。
- 被依赖：入口（作业、计数、员工投影的钩子）。

## 测试

`itest/delivery_integration_test.go`（投递、重复、结果事件、到期）、`itest/routing_integration_test.go`（偏好与必须送达、岗位展开、等投影）。
