# infra/notification

通知组件的代码。用法与边界在 `BRICKKIT.md`，为什么这样设计（术语、数据、规则、未决问题）在 [docs/design.md](docs/design.md)；跨组件的规则在装配项目 be-assembly-standard 的协议 10（通知）、协议 01（后台工作）、协议 02（投影）与决定 D68、A83；意图的契约与发送方的行为在线协议第 18 章。

## 代码地图

代码按业务模块分层。**先读这一节，再进目录**：每个代码目录都有自己的 `AGENTS.md`（同目录的 `CLAUDE.md` 让 Claude Code 读这个目录的文件时自动带上它），写这一层负责什么、不负责什么、每个文件、依赖谁、测在哪。

| 目录 | 是什么 |
| --- | --- |
| [`notification/`](notification/AGENTS.md) | 入口：组件定义与组装（路由、按配置的订阅、员工投影、作业、恢复核对，转给业务模块）；不写业务。`main.go` 与外壳只引用它 |
| [`internal/`](internal/AGENTS.md) | 业务：下面的模块各一个包；模块之间的依赖表在它的 `AGENTS.md` |
| [`itest/`](itest/AGENTS.md) | 集成测试，按业务流程分文件；它的 `AGENTS.md` 是测试索引 |
| `notification/migrations/` | 表：意图、合并键、站内消息、偏好、投递、被拒的意图 |
| `cmd/seed/` | 种子（`make seed`）：角色与授予（站内消息与偏好给所有种子用户，被拒的意图给种子管理员） |
| `contract/` | 本组件发布的契约：`contract/openapi.yaml`、`contract/events/`（投递结果） |
| `docs/design.md` | 设计（NT01–NT12） |

业务模块（`internal/` 下）：

| 模块 | 负责 | 拥有的表 |
| --- | --- | --- |
| [`intents`](internal/intents/AGENTS.md) | 收意图：发布者核对、按共享意图契约核对形状、去重、合并键的序号与取代（撤回站内消息）；被拒的意图 | `intent`（写入）、`collapse`、`rejected_intent` |
| [`delivery`](internal/delivery/AGENTS.md) | 投递：岗位展开、按类别与偏好路由、写站内消息、记结果、到期、投递结果事件、计数与告警、清理 | `message`（写入）、`delivery` |
| [`inbox`](internal/inbox/AGENTS.md) | 用户面：我的站内消息、未读数、标为已读 | `message`（读与标记） |
| [`preferences`](internal/preferences/AGENTS.md) | 用户面：我的偏好、修改 | `preference` |
| [`sources`](internal/sources/AGENTS.md) | 部署的发送方配置：格式、订阅的流与事件类型（纯函数） | — |

共用的：[`core`](internal/core/AGENTS.md)（名字、资源与操作、类别目录、投递结果、参数、错误与校验、游标、`Base` 与目录接口）。

| 想改 | 从这里开始 | 然后 |
| --- | --- | --- |
| 谁的意图能进来；被拒的原因 | `docs/design.md` 的 NT01、NT02；线协议 P18.8 | `internal/intents` 的 `Admit`（纯函数，对线协议的向量）、`OnEvent` |
| 后到的意图取代先到的、撤回 | `docs/design.md` 的 NT04；线协议 P18.7 | `internal/intents` 的 `OnEvent`、`supersede` |
| 谁收到、在哪个渠道、偏好与必须送达 | `docs/design.md` 的 NT03、NT05–NT07 | `internal/core` 的 `Catalog`；`internal/delivery` 的 `Route`（纯函数，对向量）、`deliver` |
| 岗位怎样展开 | `docs/design.md` 的 NT06 | `internal/delivery` 的 `expand`；SDK 的 `flow.Employees` |
| 加一个类别 | 线协议 P18.3（目录是协议的，先改协议） | `internal/core` 的 `Catalog`、`contract/openapi.yaml` 的 `Category` |

## 构建与测试

- `go test ./...`（`make test`）：清单的 `configSchema` 与 `events`、OpenAPI 与路由、合并安全（`notification/contract_test.go`）；配置的格式（`internal/sources`）；`Admit` 与 `Route` 对线协议的向量 `notification-intent`（`internal/intents`、`internal/delivery`）。
- `make test-integration`（`go test -tags integration ./itest/`）：真实的 PostgreSQL 与消息系统。
- `brickkit lint`

## 设计取舍

- **意图是事件，不是调用**：发送方在自己的流上经发件表发出，本组件按配置订阅、核对发布者（同统一待办）；发送方不需要知道本组件在不在。
- **合并按序号，不按到达顺序**：合并键的最大序号单独一张表，保留得比意图与消息都久；迟到的旧意图直接作废，撤回先到也撤得住。
- **岗位在投递时展开**：按 SDK 的员工投影（带水位），投影不完整时等，不按旧数据投。
- **数据范围就是“收件人是我”**：消息与偏好的资源没有维度；每条查询都带调用者本人。

## 易错点

- 不要用投影里的发送方：发送方组件必须等于信封的来源组件（消息系统按服务身份限制谁能在哪个流上发布）。
- 不要让投递重写站内消息：`delivery` 以“意图 + 收件人 + 渠道”唯一，已有结果的跳过。
- 合并键第一次出现时没有行可锁：先插占位行（序号 0）再 `FOR UPDATE`，两个并发的首个意图才会依次判断。

## 改代码前自查

- 改了版本号吗？每次改动都升版本。

## BrickKit

这是一个 BrickKit 组件：平台只读 `component.yaml`。它依赖的规则：

- `configSchema` 的键就是代码读的环境变量名。不能用保留名：`COMPONENT_ID`、`COMPONENT_VERSION`、`PORT`、`BRICKKIT_SERVED_MEMBERS`、`BRICKKIT_SERVED_MEMBERS_CONFIG`，以及任何 `*_ENDPOINT`。
- 依赖写精确版本。依赖的地址以 `<ID>_ENDPOINT` 注入；缺席的可选依赖根本没有这个变量，读的时候要带兜底。
- `/healthz` 只查本进程，不查依赖。迁移命令用同一个镜像跑，遇到不认识的参数必须直接失败；`local.migrationCommand` 从源码跑同一条迁移，给 `mode: local` 用。
- `BRICKKIT.md` 会随版本进入每个使用它的项目，在那里是脱离仓库单独读的：跟代码一起改，不放相对链接。
- 发版：改 `metadata.version`，提交、推送，`brickkit release`。`brickkit lint` 会检查清单和这些文档——在项目里、在这个目录下跑，只查这个组件（`--all` 查整个项目）。
- 完整规则在 `brickkit-component` 技能里（装了技能的项目或仓库根目录下的 `.claude/skills/brickkit-component/SKILL.md`；`brickkit skills update` 会装上）；参数问 `brickkit <命令> --help`；BrickKit 自己的文档用 `brickkit docs` 看。
