// Package partition 是 notification_records 的月分区维护——本组件唯一
// 按月分区的表（设计计划 §7：它是本组件唯一会无限增长的表，量级是
// infra-workflow 待办的几倍）。同 erp-inventory
// backend/internal/partition/monthly.go 的既有实现，边界算法从"周一"
// 换成"月初"，分区名格式（"表名_YYYY_MM_01"）必须与迁移里手写的初始
// 分区一致（同 erp-inventory 既有教训：两处分区命名代码不一致会导致
// 后台任务尝试新建已存在时间范围的分区，撞上"分区范围不许重叠"报错）。
package partition

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
)

// checkInterval 与 partition.go 共用（同一个包，避免重复声明）。
const lookAheadMonths = 3 // 提前建好当前月 + 未来 3 个月

var monthlyPartitionedTables = []string{"notification_records"}

// StartMonthly 立刻检查一次，之后每 24 小时检查一次。单次失败只记日志，
// 不让循环退出（同 module.Start 的其余后台循环一致的容错方式）。
func StartMonthly(ctx context.Context, db *sql.DB, role, schema string, logger *slog.Logger) error {
	if err := ensureAllMonthly(ctx, db, role, schema); err != nil {
		logger.Error("月分区维护失败", "error", err)
	}

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := ensureAllMonthly(ctx, db, role, schema); err != nil {
				logger.Error("月分区维护失败", "error", err)
			}
		}
	}
}

func ensureAllMonthly(ctx context.Context, db *sql.DB, role, schema string) error {
	return besdk.WithTx(ctx, db, role, schema, func(tx *sql.Tx) error {
		monthStart := firstOfMonth(time.Now().UTC())
		for i := 0; i <= lookAheadMonths; i++ {
			from := monthStart.AddDate(0, i, 0)
			to := from.AddDate(0, 1, 0)
			for _, table := range monthlyPartitionedTables {
				if err := ensurePartition(ctx, tx, table, monthPartitionName(table, from), from, to); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func monthPartitionName(table string, from time.Time) string {
	return fmt.Sprintf("%s_%04d_%02d_01", table, from.Year(), from.Month())
}

func firstOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// ⚠️ 不需要额外 ALTER TABLE ... OWNER TO：这段代码跑在 besdk.WithTx 已经
// SET LOCAL ROLE 到 infra_notification_rw 之后，CREATE TABLE 建出来的
// 分区所有者就是当前角色本身（真机验证过：SET LOCAL ROLE x 之后
// CREATE TABLE，pg_tables.tableowner 直接是 x，不需要再转一次）。migrations/
// 001_create_notification.up.sql 里迁移建的初始分区所有者虽然是运行
// 迁移的管理凭据（不是 infra_notification_rw），但 infra_notification_rw
// 在这个 schema 上有 ALTER DEFAULT PRIVILEGES 授予的 SELECT/INSERT/
// UPDATE/DELETE（be-ops 建 schema 时配的默认权限），DML 不需要"是不是
// owner"——两条路径殊途同归，都不需要这一步（ensurePartition 本体见
// partition.go，周/月两套维护共用同一份"存不存在就建"的逻辑）。
