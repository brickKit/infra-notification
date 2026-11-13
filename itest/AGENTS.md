# itest/：集成测试与测试索引

通知组件对着真实的 PostgreSQL 与消息系统、经它真实的接口的测试（L2）。构建标签 `integration`；一条业务流程一个文件。

## 怎样跑

- `make test-integration`（`go test -count=1 -tags integration ./itest/`）；环境变量 `BE_SDK_TEST_PG_ADMIN`、`BE_SDK_TEST_NATS_URL`（装配项目的 `itenv.sh`）。
- 测试每次删掉再建 schema `infra_notification_it`；删掉再建测试自己的流 `test.buy`、`test.pay`、`test.evil`（不碰任何真实组件的流）；端口 28202、9202 与假渠道成员的 19207 要空着；部署配置了钉钉渠道（`dingtalk=integration/im-dingtalk`）。
- 发送方由测试自己扮演：在测试组件的流上发意图，信封的来源通常就是那个组件（`publishAs` 发冒充来源的）；`test/buy`、`test/pay` 是部署配置的发送方，`test/evil` 不是。岗位按测试自己设的目录展开（按岗位记投影完整与否）。**真实的员工投影（`flow.Employees`）与它的变化钩子没有自动测试**：格里的意图都来自流程内核，收件人已经解析成主体；第一个发岗位收件人的发送方上线时在格里测。
- **只有一个测试** `TestNotificationEndToEnd`，各步的顺序是 `delivery`、`merging`、`preferences`、`positions`、`expiry`、`externals`、`inbox`、`senderCheck`、`recoveryChecks`（后面的步用到前面留下的消息）。

## 文件

| 文件 | 步 | 测的流程 | 规则 |
| --- | --- | --- | --- |
| `delivery_integration_test.go` | `delivery`、`merging`、`expiry` | 两个主体各一条消息、同一主体点两次只一条、未读数、重复的意图不再投、每个意图一条结果事件；新序号撤回旧消息、撤回意图、迟到的旧序号作废、撤回先到时原意图不写消息；到期：主体照投、等投影的岗位记过期、意图收尾 | NT04、NT05、NT10；P18.6、P18.7、P18.9 |
| `routing_integration_test.go` | `preferences`、`positions` | 目录与默认、关掉的类别不投且记结果、必须送达的关不掉也照投、营销默认关、坏的类别与渠道 400；岗位展开为持有人（记已展开）、本人与岗位只一条、没人持有记展开不了、投影不完整时等、完整后投给当时的持有人；还没投就被新序号取代的不投；岗位展开过就不再展开（之后换的人收不到） | NT03、NT04、NT06、NT07、NT09 |
| `external_integration_test.go` | `externals` | 审批待办投到站内与钉钉（测试里的假渠道成员 `fakeChannel`，经 mTLS 答 `be.channel.v1`）：持有人的授权只交给接受动作的渠道、没有账号答“没有渠道账号”而站内照写；渠道不可达时重试用同一个投递身份、站内不等、结果事件等外部投递；没有外部渠道的类别；偏好关掉钉钉（必须送达的类别也能关钉钉，站内关不掉）；被取代、到期的待投不再交出、被取代后结果事件照发 | NT04、NT07、NT09、NT10、NT13、NT14；P20.1、P20.3、P20.4 |
| `inbox_integration_test.go` | `inbox` | 只看自己的、只列未读、标为已读留第一次的时刻、别人的 404、全部已读只改自己的、未读数不算已撤回的、游标翻页覆盖整个列表、没有授予被拒、坏参数 400 | NT08 |
| `senders_integration_test.go` | `senderCheck`、`recoveryChecks` | 在自己的流上冒充别人（来源与发送方都写成别人）、发送方不是流的属主、不合契约、类别不在目录、到期早于发生都被拒、不投、列给管理员、各有一条安全审计（重投不重复记）；没配的发送方不被订阅；恢复核对只许恢复工具 | NT01、NT02；P18.8；线协议第 14 章 |

共用的：

| 文件 | 内容 |
| --- | --- |
| `endtoend_integration_test.go` | `TestNotificationEndToEnd`；`fixture`（运行中的成员、假目录、Alice 全部操作、Bob 与 Carol 只有自己的消息与偏好、Dave 没有授予）；`start`；发送方一侧的 `intent`、`principal`、`position`、`posKey`、`withdraw`、`send`、`publish`、`publishAs`，等待与读取的 `taken`、`ended`、`results`（站内）、`messagesOf`；`client.do`、`portFree` |

## 加测试

新流程加一个文件，在上表加一行；测试不为了通过而改。
