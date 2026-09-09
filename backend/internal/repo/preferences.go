package repo

import (
	"context"
	"database/sql"
	"fmt"

	besdk "github.com/brickKit/be-sdk-go"
)

// criticalCategories：设计计划 §2.1——critical 是"类别"的属性，不是
// "通道"的属性。本阶段只有一个类别，"两者放一个组件里写"式地硬编码在
// 这里；等真的出现第二个 critical 类别时再考虑要不要挪成配置（同 SOP-P
// 判据：现在只有一条，配置化是提前抽象）。
var criticalCategories = map[string]bool{
	CategoryWorkflowTask: true,
}

// CategoryWorkflowTask 是本阶段唯一的通知来源类别（infra-workflow 的
// 审批待办），设计计划 §2.1 表格里标记为 critical：用户能选走哪个通道，
// 但不能选择一个都不走。
const CategoryWorkflowTask = "workflow_task"

func IsCriticalCategory(category string) bool { return criticalCategories[category] }

// defaultGlobalChannels 是用户从未设置过任何偏好时的隐含全局开关。
// ⚠️ 刻意只有 IM，不是 proto 里全部三个 Channel——EMAIL/SMS 本阶段没有
// 真实适配器（设计计划 §4 事件表），默认把它们打开只会造出一堆永远停在
// PENDING、没有适配器会消费的死记录。用户可以自己在偏好里显式加上
// EMAIL/SMS（那是他自己的选择，后果自负），但不该是平台的默认值。
func defaultGlobalChannels() []string { return []string{ChannelIM} }

type Preferences struct {
	Sub              string
	GlobalChannels   []string
	CategoryChannels map[string][]string
}

// loadPreferenceRowTx 读一层（category=="" 是全局层，具体分类名是分类层，
// 见 001 迁移的表注释）。hasRow=false 表示这一层这个用户没设置过，调用方
// 决定怎么兜底。
func loadPreferenceRowTx(ctx context.Context, tx *sql.Tx, sub, category string) (channels []string, hasRow bool, err error) {
	var raw string
	err = tx.QueryRowContext(ctx,
		`SELECT channels::text FROM notification_preferences WHERE sub = $1 AND category = $2`,
		sub, category).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return scanTextArray(raw), true, nil
}

func intersect(a, ceiling []string) []string {
	allowed := make(map[string]bool, len(ceiling))
	for _, c := range ceiling {
		allowed[c] = true
	}
	out := make([]string, 0, len(a))
	for _, c := range a {
		if allowed[c] {
			out = append(out, c)
		}
	}
	return out
}

// ResolvePreferredChannelsTx 是发通知时真正用的解析入口（设计计划
// §2.1 两层模型 + critical 兜底）：
//  1. 全局层是天花板——没设置过就是 defaultGlobalChannels()；
//  2. 分类层是子集——没设置过就"跟随全局"（proto Preferences.category_channels
//     字段注释原文），设置过就与全局层取交集（不能靠分类层突破全局关闭）；
//  3. critical 类别解出来是空集时兜底成 defaultGlobalChannels()——
//     "不可关闭"落地成"这里不会真的返回空"，而不是在别处特判。
//
// ⚠️ 这个兜底不覆盖"收件人被停用"（user_contacts.disabled）那种情况——
// 那是调用方（consumer）在拿到这个函数的返回值之后，另外根据
// user_contacts 做的一次覆盖判断，两者是不同层次的判断，不要合并到一起
// （合并了会让"停用用户还能通过 critical 兜底收到通知"这个真实的坑很难
// 联想到是这里）。
func ResolvePreferredChannelsTx(ctx context.Context, tx *sql.Tx, sub, category string) ([]string, error) {
	globalChannels, hasGlobal, err := loadPreferenceRowTx(ctx, tx, sub, "")
	if err != nil {
		return nil, fmt.Errorf("读全局通道偏好: %w", err)
	}
	ceiling := defaultGlobalChannels()
	if hasGlobal {
		ceiling = globalChannels
	}

	catChannels, hasCat, err := loadPreferenceRowTx(ctx, tx, sub, category)
	if err != nil {
		return nil, fmt.Errorf("读分类通道偏好: %w", err)
	}
	resolved := ceiling
	if hasCat {
		resolved = intersect(catChannels, ceiling)
	}

	if IsCriticalCategory(category) && len(resolved) == 0 {
		resolved = defaultGlobalChannels()
	}
	return resolved, nil
}

// GetPreferences 是 GET /preferences 与 gRPC GetPreferences 共用的读入口。
// 没设置过的层在返回值里体现为"跟随默认/跟随全局"（GlobalChannels 填
// defaultGlobalChannels()，对应分类不出现在 CategoryChannels 里）——同
// ResolvePreferredChannelsTx 的兜底口径，保证用户在偏好页面看到的
// "生效中的通道"与真的发通知时用的解析结果一致。
func (r *Repo) GetPreferences(ctx context.Context, sub string) (*Preferences, error) {
	p := &Preferences{Sub: sub, CategoryChannels: map[string][]string{}}
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT category, channels::text FROM notification_preferences WHERE sub = $1`, sub)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var category, raw string
			if err := rows.Scan(&category, &raw); err != nil {
				return err
			}
			channels := scanTextArray(raw)
			if category == "" {
				p.GlobalChannels = channels
			} else {
				p.CategoryChannels[category] = channels
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, wrap("读通道偏好", err)
	}
	if p.GlobalChannels == nil {
		p.GlobalChannels = defaultGlobalChannels()
	}
	return p, nil
}

// SetPreferences 整份替换（同 REST PUT 语义，见 be-sdk-go v0.2.4 修复
// 时顺带核对过的 command_idempotency 结论：这个写法天然幂等，重复提交
// 同一份内容得到同一个结果，不需要 idempotency_key）。
//
// ⚠️ critical 类别显式提交空数组会被拒绝——这是 openapi 契约里 PUT
// /preferences 400 那一档的服务端强制，不是前端隐藏几个复选框就够
// （设计计划 §2.1）。没提交某个 critical 类别（"跟随全局"）不受这条限制：
// 即使全局也是空，ResolvePreferredChannelsTx 的兜底会接住。
func (r *Repo) SetPreferences(ctx context.Context, sub string, globalChannels []string, categoryChannels map[string][]string) (*Preferences, error) {
	for cat, chans := range categoryChannels {
		if IsCriticalCategory(cat) && len(chans) == 0 {
			return nil, fmt.Errorf("%w: 分类 %q 是关键通知，不能清空全部通道", ErrInvalidArgument, cat)
		}
	}
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM notification_preferences WHERE sub = $1`, sub); err != nil {
			return fmt.Errorf("清空旧偏好: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO notification_preferences (sub, category, channels) VALUES ($1, '', $2)`,
			sub, globalChannels); err != nil {
			return fmt.Errorf("写全局通道偏好: %w", err)
		}
		for cat, chans := range categoryChannels {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO notification_preferences (sub, category, channels) VALUES ($1, $2, $3)`,
				sub, cat, chans); err != nil {
				return fmt.Errorf("写分类通道偏好 %q: %w", cat, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, wrap("写通道偏好", err)
	}
	return r.GetPreferences(ctx, sub)
}
