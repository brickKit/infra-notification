# notification/：入口与组装

组件对外的一个包：`main.go` 与外壳只引用它（`notification.Definition()`、`notification.New`）。这里只**组装**：把 `internal/` 下业务模块的函数登记成 SDK 要的路由、订阅、作业与恢复核对；业务一行也不写。

## 负责与不负责

- 负责：组件的身份与版本、自己的配置键（`NOTIFICATION_INTENT_SOURCES`）、迁移、流（投递结果）、把哪个路由交给哪个模块、按配置建哪些订阅、装了 `mdm/employee` 时运行员工投影。
- 不负责：业务规则（去 [internal/AGENTS.md](../internal/AGENTS.md) 找模块）；名字（`internal/core/names.go`）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `definition.go` | `Version`、端口、迁移、`Keys` 与 `Own`；`Definition()`（流 `infra.notification`、补发的操作） |
| `module.go` | `Module` 与 `New`（读配置：格式不对成员不启动；有 `mdm/employee` 时建员工投影，它是岗位的目录，变化时排一轮投递）；`UseDirectory`（集成测试换目录）；`Start`（运行员工投影）；`Routes`（六条用户面路由）、`Subscriptions`（配置里每个发送方的流 → `intents.OnEvent`；员工的流 → 员工投影）、`Jobs`（立即的一轮、每分钟一轮、每天的清理） |
| `checks.go` | `RecoveryChecks`：没有自己的检查（意图由补发重来；SDK 自带的检查覆盖消费进度） |
| `contract_test.go` | 单元测试：清单的 `configSchema` 与 `events`、OpenAPI 与路由一一对应、合并安全 |

迁移在 `migrations/`。

## 依赖与被依赖

- 依赖：`internal/` 下全部模块；SDK 的 `flow`（员工投影）。
- 被依赖：`main.go`、外壳、`itest/`。

## 测试

`go test ./notification/`。
