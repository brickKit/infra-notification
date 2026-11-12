# core：全组件共用的名字与写法（共用）

每个模块都引用、自己什么业务也不做的东西。

## 负责与不负责

- 负责：组件 ID、流与投递结果的事件类型、配置键（`NOTIFICATION_INTENT_SOURCES`）、资源与操作、渠道、类别目录（`Catalog`、`CategoryOf`，线协议 P18.3 的目录与 NT03 的默认）、被拒的原因、投递结果、参数（批大小、每分钟一轮、保留期）（`names.go`）；资源定义 `Definitions()`（没有维度）；授权（`Check`）、校验（`Invalid`、`NotFound`）、游标与页大小（`rules.go`）；`Base` 与岗位的目录接口 `Directory`（`base.go`）。
- 不负责：任何业务规则。

## 文件

| 文件 | 内容 |
| --- | --- |
| `names.go` | 常量；`Category`、`Catalog`、`CategoryOf`；`Definitions()` |
| `rules.go` | `Check`、`Invalid`、`NotFound`、`Cursor`、`FromCursor`、`Limit` |
| `base.go` | `Base`（运行时、配置的发送方、目录）；`Directory`（SDK 的 `flow.Employees` 实现它，集成测试换成假的） |

## 依赖与被依赖

- 依赖：只有 SDK。
- 被依赖：全部模块与入口。

## 测试

入口的单元测试、`Admit` 与 `Route` 的向量测试与全部流程测试间接覆盖。
