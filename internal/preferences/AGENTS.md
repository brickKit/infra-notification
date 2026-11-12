# preferences：我的偏好

用户面（`docs/design.md` 的 NT07、NT09）。

## 负责与不负责

- 负责：`Mine`（目录里每个类别 × 渠道的开关、默认、能不能改）、`Set`（只对可关的类别；不在目录的类别或渠道、必须送达的类别答 `REQUEST_INVALID`）。
- 不负责：投递时怎样用偏好（`delivery.Route`）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `preferences.go` | `Preference`、`Mine`、`Set` |

## 依赖与被依赖

- 依赖：`core`。
- 被依赖：入口（路由）。

## 测试

`itest/routing_integration_test.go` 的 `preferences`。
