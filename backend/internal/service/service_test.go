// 数据权限边界测试（总纲 SOP-W-8"权限/数据权限边界测试"）：本组件的
// owner 维是强制注入，不是"查询到之后再判断"——ListMyRecords 把
// in.RecipientSub 直接覆盖成 besdk.ScopeOf(ctx).Owner，调用方传什么都
// 不算数。这条测试真的往输入里塞了别人的 sub，验证的正是"覆盖"这件事
// 真的发生了，不是恰好输入本来就对。
package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	besdk "github.com/brickKit/be-sdk-go"
	"github.com/brickKit/infra-notification/backend/internal/repo"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("未设置 TEST_PG_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

var seq int64

func uniqueSuffix(prefix string) string {
	n := atomic.AddInt64(&seq, 1)
	return fmt.Sprintf("%s-%d", prefix, n)
}

// createRecordForRecipient 直接走 repo 层给指定的人建一条通知记录，
// 供边界测试用——不需要真的经过事件消费流程。db/role/schema 跟
// repo.New(db, "infra_notification_rw", "infra_notification") 传的是
// 同一份，CreateRecordTx 只要求一个 *sql.Tx，不依赖 *repo.Repo 本身。
func createRecordForRecipient(t *testing.T, db *sql.DB, recipientSub string) int64 {
	t.Helper()
	var id int64
	err := besdk.WithTx(context.Background(), db, "infra_notification_rw", "infra_notification", func(tx *sql.Tx) error {
		var innerErr error
		id, innerErr = repo.CreateRecordTx(context.Background(), tx, repo.CreateRecordInput{
			RecipientSub: recipientSub, Category: "workflow_task", Channel: repo.ChannelIM,
			Title: "边界测试通知", Body: "body", SourceComponent: "test", SourceAggregate: "test", SourceID: uniqueSuffix("src"),
		}, repo.StatusPending)
		return innerErr
	})
	if err != nil {
		t.Fatalf("建测试通知记录失败: %v", err)
	}
	return id
}

func TestListMyRecords_别人的通知看不到_即使故意传了别人的sub(t *testing.T) {
	db := testDB(t)
	r := repo.New(db, "infra_notification_rw", "infra_notification")
	svc := New(r, slog.Default())

	subA := uniqueSuffix("u_A")
	subB := uniqueSuffix("u_B")
	idA := createRecordForRecipient(t, db, subA)
	idB := createRecordForRecipient(t, db, subB)

	ctxA := besdk.ContextWithClaims(context.Background(), besdk.Claims{Sub: subA})

	// 故意在输入里塞 B 的 sub，验证 ListMyRecords 真的会覆盖它，不是
	// 恰好输入本来就对——这是数据权限边界测试要验的那条"覆盖真的发生了"。
	records, _, err := svc.ListMyRecords(ctxA, repo.ListInput{RecipientSub: subB, PageSize: 100})
	if err != nil {
		t.Fatalf("ListMyRecords 失败: %v", err)
	}

	sawA, sawB := false, false
	for _, rec := range records {
		if rec.ID == idA {
			sawA = true
		}
		if rec.ID == idB {
			sawB = true
		}
		if rec.RecipientSub != subA {
			t.Fatalf("以 A 的身份查询，结果里出现了不属于 A 的记录：%+v", rec)
		}
	}
	if !sawA {
		t.Fatal("A 自己的通知应该能看到")
	}
	if sawB {
		t.Fatal("B 的通知不该出现在 A 的列表里——即使输入故意传了 B 的 sub")
	}
}
