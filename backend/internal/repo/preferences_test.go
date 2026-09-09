package repo

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sort"
	"testing"

	besdk "github.com/brickKit/be-sdk-go"
)

func resolveInOwnTx(t *testing.T, r *Repo, sub, category string) []string {
	t.Helper()
	var got []string
	err := besdk.WithTx(context.Background(), r.db, r.role, r.schema, func(tx *sql.Tx) error {
		channels, err := ResolvePreferredChannelsTx(context.Background(), tx, sub, category)
		got = channels
		return err
	})
	if err != nil {
		t.Fatalf("ResolvePreferredChannelsTx 失败: %v", err)
	}
	return got
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// TestResolvePreferredChannelsTx_未设置偏好时默认只有IM 验证 defaultGlobalChannels
// 的口径：从没设置过偏好的用户，任何类别都解析成 [IM]，不是 proto 里
// 全部三个 Channel——EMAIL/SMS 本阶段没有真实适配器，默认打开只会造出
// 死记录（preferences.go defaultGlobalChannels 注释）。
func TestResolvePreferredChannelsTx_未设置偏好时默认只有IM(t *testing.T) {
	r := testRepo(t)
	sub := uniqueID("sub")

	got := resolveInOwnTx(t, r, sub, CategoryWorkflowTask)
	if !reflect.DeepEqual(got, []string{ChannelIM}) {
		t.Fatalf("期望默认只有 IM，实际 %v", got)
	}

	got = resolveInOwnTx(t, r, sub, "some_noncritical_category")
	if !reflect.DeepEqual(got, []string{ChannelIM}) {
		t.Fatalf("非 critical 类别没设置过偏好时也该跟随全局默认，实际 %v", got)
	}
}

// TestResolvePreferredChannelsTx_critical类别在全局全关时兜底 是设计计划
// §2.1 的核心断言：critical 类别不可能被完全关闭，即使用户把全局通道
// 开关清空，workflow_task 仍然要解析出至少一个通道；而非 critical 类别
// 在同样条件下真的会解析成空集（对照组，证明兜底只对 critical 生效，
// 不是代码碰巧总返回非空）。
func TestResolvePreferredChannelsTx_critical类别在全局全关时兜底(t *testing.T) {
	r := testRepo(t)
	sub := uniqueID("sub")

	if _, err := r.SetPreferences(context.Background(), sub, []string{}, nil); err != nil {
		t.Fatalf("清空全局通道失败: %v", err)
	}

	critical := resolveInOwnTx(t, r, sub, CategoryWorkflowTask)
	if len(critical) == 0 {
		t.Fatal("critical 类别不该解析出空集——这是设计计划 §2.1 的硬约束")
	}

	nonCritical := resolveInOwnTx(t, r, sub, "some_noncritical_category")
	if len(nonCritical) != 0 {
		t.Fatalf("对照组：非 critical 类别在全局全关时应该真的解析成空集，实际 %v（如果这里也非空，说明兜底逻辑误伤了不该兜底的类别）", nonCritical)
	}
}

// TestResolvePreferredChannelsTx_分类层不能突破全局天花板 验证"全局覆盖
// 分类"的优先级（设计计划 §2.1，借鉴 Novu 的口径）：即使分类层显式选了
// EMAIL，只要全局层没开 EMAIL，解析结果里就不该出现 EMAIL。
func TestResolvePreferredChannelsTx_分类层不能突破全局天花板(t *testing.T) {
	r := testRepo(t)
	sub := uniqueID("sub")

	if _, err := r.SetPreferences(context.Background(), sub, []string{ChannelIM},
		map[string][]string{CategoryWorkflowTask: {ChannelIM, ChannelEmail}}); err != nil {
		t.Fatalf("写偏好失败: %v", err)
	}

	got := resolveInOwnTx(t, r, sub, CategoryWorkflowTask)
	if !reflect.DeepEqual(sorted(got), []string{ChannelIM}) {
		t.Fatalf("分类层选了全局没开的 EMAIL，期望被过滤掉只剩 IM，实际 %v", got)
	}
}

// TestSetPreferences_critical类别不能清空全部通道 是 openapi 契约里 PUT
// /preferences 400 那一档的服务端强制（设计计划 §2.1，"critical 是类别
// 的属性"落到写路径上的具体校验）。
func TestSetPreferences_critical类别不能清空全部通道(t *testing.T) {
	r := testRepo(t)
	sub := uniqueID("sub")

	_, err := r.SetPreferences(context.Background(), sub, []string{ChannelIM},
		map[string][]string{CategoryWorkflowTask: {}})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("期望 ErrInvalidArgument，实际 %v", err)
	}
}

// TestSetPreferences_整份替换 验证 PUT 的天然幂等语义（同 command_idempotency
// 去掉时的既有结论：SetPreferences 是"设成这个值"，不是增量合并）——
// 第二次调用如果没带第一次设置的某个分类，那个分类要恢复"跟随全局"，
// 不能残留第一次的值。
func TestSetPreferences_整份替换(t *testing.T) {
	r := testRepo(t)
	sub := uniqueID("sub")
	ctx := context.Background()

	if _, err := r.SetPreferences(ctx, sub, []string{ChannelIM, ChannelEmail},
		map[string][]string{CategoryWorkflowTask: {ChannelIM}, "other_cat": {ChannelEmail}}); err != nil {
		t.Fatalf("第一次写偏好失败: %v", err)
	}

	p, err := r.SetPreferences(ctx, sub, []string{ChannelIM}, map[string][]string{CategoryWorkflowTask: {ChannelIM}})
	if err != nil {
		t.Fatalf("第二次写偏好失败: %v", err)
	}
	if _, ok := p.CategoryChannels["other_cat"]; ok {
		t.Fatalf("第二次没带 other_cat，期望被整份替换掉，实际仍残留: %v", p.CategoryChannels)
	}
	if !reflect.DeepEqual(p.GlobalChannels, []string{ChannelIM}) {
		t.Fatalf("全局通道期望只剩 IM，实际 %v", p.GlobalChannels)
	}
}

// TestGetPreferences_默认值 验证从没设置过偏好的用户读到的是隐含默认值，
// 不是空结构体——这条断言保证"用户在偏好页面看到的生效中的通道"与
// ResolvePreferredChannelsTx 真的发通知时用的解析结果口径一致。
func TestGetPreferences_默认值(t *testing.T) {
	r := testRepo(t)
	sub := uniqueID("sub")

	p, err := r.GetPreferences(context.Background(), sub)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.GlobalChannels, []string{ChannelIM}) {
		t.Fatalf("期望默认全局通道是 [IM]，实际 %v", p.GlobalChannels)
	}
	if len(p.CategoryChannels) != 0 {
		t.Fatalf("期望没有任何分类覆盖，实际 %v", p.CategoryChannels)
	}
}
