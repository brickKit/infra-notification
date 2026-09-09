package repo

import "strings"

// scanTextArray 手解 PostgreSQL text[] 字面量（"{a,b}" 这种格式，空数组
// 是 "{}"）。
//
// ⚠️ pgx/v5 的 database/sql 通用接口不会自动把 text[] 解成 []string——
// Scan 到 *[]string 直接报 "unsupported Scan, storing driver.Value type
// string into type *[]string"，真机测过（同 infra-authz bundle.go
// loadRolesInto 的既有教训，那边是 array_agg 的聚合结果，这里是
// notification_preferences.channels 这个真实存储的数组列，成因不同但
// 症状一样）。只能把目标类型定成 string 接住原始文本，自己解。
//
// ⚠️ 只处理"读出来"这一侧：写入方向不需要对应的 encode 函数——pgx 能直接
// 把 []string 当查询参数编码成 PostgreSQL 数组（同 erp-inventory
// repo.go 的既有判据，真机验证过 INSERT/UPDATE 两处都可以直接传
// []string）。
//
// 本组件的数组值只会是 Channel 常量（IM/EMAIL/SMS），不含逗号或大括号，
// 简单按逗号切分足够，不需要处理带引号转义的通用 PostgreSQL 数组语法。
func scanTextArray(raw string) []string {
	raw = strings.TrimPrefix(raw, "{")
	raw = strings.TrimSuffix(raw, "}")
	if raw == "" {
		return []string{}
	}
	return strings.Split(raw, ",")
}
