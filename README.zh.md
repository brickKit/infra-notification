[English](README.md) · [中文](README.zh.md)

# infra/notification

接收业务组件的"通知意图"，按每个用户的通道偏好路由给已安装的通道适配器。

## 在项目里使用

```bash
brickkit add infra/notification@2.0.0
brickkit up
```

先看 BRICKKIT.zh.md 的"部署前准备"（PostgreSQL 的 schema 与登录角色、NATS、权限与 JWKS 地址，以及真正投递要用的 IM 适配器，例如 `integration/im-dingtalk`）。

## 文档

| 想知道 | 读 |
|---|---|
| 它做什么、不做什么，怎么配置，部署前要准备什么 | [BRICKKIT.zh.md](BRICKKIT.zh.md) |
| gRPC、REST 与事件契约 | [contracts/](contracts/) |
| 为什么这样设计：边界、偏好模型、投递状态、未决问题 | [docs/design.zh.md](docs/design.zh.md) |
| 依赖（无）与配置键 | [component.yaml](component.yaml) |
| 怎么开发：代码地图、测试、易错点 | [AGENTS.zh.md](AGENTS.zh.md) |

## 开发

Go 1.25、Gin、`database/sql` + pgx，迁移经 be-sdk-go 用 golang-migrate。测试需要真实的 PostgreSQL（`TEST_PG_DSN`）与 NATS（`TEST_NATS_URL`）；命令与成功的样子见 AGENTS.zh.md 的"构建与测试"。在 BrickEnterprise 项目里，项目根 `make verify ID=infra/notification FOCUS=1` 以容器和本机进程两种形态真机跑一遍，结束后自动收尾。
