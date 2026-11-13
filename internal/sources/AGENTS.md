# sources：部署的发送方

`NOTIFICATION_INTENT_SOURCES` 的格式与它推出的订阅（`docs/design.md` 的 NT01）。纯函数。

## 负责与不负责

- 负责：`Parse`（逗号分隔的组件 ID；格式不对、重复：成员不启动；答复排好序）；`Channels`（`NOTIFICATION_CHANNELS`：“渠道名=组件”，格式不对、重名、`in_app`：成员不启动）；`Stream`、`Type`（发送方的流与意图的事件类型，线协议 P18.2，与 SDK 的 `notify` 同一套命名）。
- 不负责：订阅本身与发布者核对（`intents`）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `sources.go` | `Parse`、`Channels`、`Stream`、`Type` |
| `sources_test.go` | 单元测试：格式、流与类型 |

## 依赖与被依赖

- 依赖：`core`；SDK 的 `notify`。
- 被依赖：`intents`、入口。

## 测试

`go test ./internal/sources/`。
