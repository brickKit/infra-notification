# itest/：集成测试与测试索引

通知组件对着真实的 PostgreSQL 与消息系统、经它真实的接口的测试（L2）。构建标签 `integration`；一条业务流程一个文件。

## 怎样跑

- `make test-integration`（`go test -count=1 -tags integration ./itest/`）；环境变量 `BE_SDK_TEST_PG_ADMIN`、`BE_SDK_TEST_NATS_URL`（装配项目的 `itenv.sh`）。
- 测试每次删掉再建 schema `infra_notification_it`；删掉再建测试自己的流 `test.buy`、`test.pay`、`test.evil`（不碰任何真实组件的流）；端口 28202、9202 要空着。
- 发送方由测试自己扮演：在测试组件的流上发意图，信封的来源就是那个组件；`test/buy`、`test/pay` 是部署配置的发送方，`test/evil` 不是。岗位按测试自己设的目录展开（员工投影要真实的 `mdm/employee`，在装配项目的格里验证）。
- **只有一个测试** `TestNotificationEndToEnd`，按下表的顺序跑各步。

## 文件

| 文件 | 步 | 测的流程 | 规则 |
| --- | --- | --- | --- |
| `delivery_integration_test.go` | `delivery`、`merging`、`expiry` | 两个主体各一条消息、同一主体点两次只一条、未读数、重复的意图不再投、每个意图一条结果事件；新序号撤回旧消息、撤回意图、迟到的旧序号作废、撤回先到时原意图不写消息；到期：主体照投、等投影的岗位记过期、意图收尾 | NT04、NT05、NT10；P18.6、P18.7、P18.9 |
| `routing_integration_test.go` | `preferences`、`positions` | 目录与默认、关掉的类别不投且记结果、必须送达的关不掉也照投、营销默认关、坏的类别与渠道 400；岗位展开为持有人、本人与岗位只一条、没人持有记展开不了、投影不完整时等、完整后投给当时的持有人 | NT03、NT06、NT07、NT09 |
| `inbox_integration_test.go` | `inbox` | 只看自己的、只列未读、标为已读留第一次的时刻、别人的 404、全部已读只改自己的、没有授予被拒、坏参数 400 | NT08 |
| `senders_integration_test.go` | `senderCheck`、`recoveryChecks` | 发送方不是来源、不合契约、类别不在目录、到期早于发生都被拒、不投、列给管理员、各有安全审计；没配的发送方不被订阅；恢复核对只许恢复工具 | NT01、NT02；P18.8；线协议第 14 章 |

共用的：

| 文件 | 内容 |
| --- | --- |
| `endtoend_integration_test.go` | `TestNotificationEndToEnd`；`fixture`（运行中的成员、假目录、Alice 全部操作、Bob 与 Carol 只有自己的消息与偏好、Dave 没有授予）；`start`；发送方一侧的 `intent`、`principal`、`position`、`withdraw`、`send`、`publish`，等待与读取的 `taken`、`ended`、`results`、`messagesOf`；`client.do`、`portFree` |

## 加测试

新流程加一个文件，在上表加一行；测试不为了通过而改。
