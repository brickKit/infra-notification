# inbox：我的站内消息

用户面（`docs/design.md` 的 NT08）。每条查询都带“收件人是我”：别人的消息答 404。

## 负责与不负责

- 负责：`Mine`（新的在前、游标分页、只列未读、答复带未读数；已撤回的照常列出、不算未读）、`MarkRead`（第一次的时刻留着）、`MarkAllRead`（只改自己的）。
- 不负责：写消息（`delivery`）、撤回（`intents`）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `inbox.go` | `Message`、`Mine`、`MarkRead`、`MarkAllRead` |

## 依赖与被依赖

- 依赖：`core`。
- 被依赖：入口（路由）。

## 测试

`itest/inbox_integration_test.go`。
