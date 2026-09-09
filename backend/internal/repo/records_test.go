package repo

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	besdk "github.com/brickKit/be-sdk-go"
)

func mustCreateRecordTx(t *testing.T, r *Repo, in CreateRecordInput, status string) int64 {
	t.Helper()
	var id int64
	err := besdk.WithTx(context.Background(), r.db, r.role, r.schema, func(tx *sql.Tx) error {
		var err error
		id, err = CreateRecordTx(context.Background(), tx, in, status)
		return err
	})
	if err != nil {
		t.Fatalf("建通知记录失败: %v", err)
	}
	return id
}

func markTx(t *testing.T, r *Repo, fn func(context.Context, *sql.Tx) error) {
	t.Helper()
	if err := besdk.WithTx(context.Background(), r.db, r.role, r.schema, func(tx *sql.Tx) error {
		return fn(context.Background(), tx)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCreateRecordTx_基本创建与查询(t *testing.T) {
	r := testRepo(t)
	sub := uniqueID("sub")
	id := mustCreateRecordTx(t, r, CreateRecordInput{
		RecipientSub: sub, Category: CategoryWorkflowTask, Channel: ChannelIM,
		Title: "测试待办", Body: "body", SourceComponent: "infra/workflow",
		SourceAggregate: "workflow_task", SourceID: "1",
	}, StatusPending)

	rec, err := r.GetRecord(context.Background(), RecordIDString(id))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusPending {
		t.Fatalf("期望 PENDING，实际 %q", rec.Status)
	}
	if rec.RecipientSub != sub {
		t.Fatalf("recipient_sub 不匹配: %q", rec.RecipientSub)
	}
}

// TestRecordStatusMachine_ACCEPTED到SENT 验证状态机 PENDING → ACCEPTED →
// SENT 的正向流转（设计计划 §2：不能从 PENDING 直接跳 SENT，中间必须
// 经过 ACCEPTED——这里用状态转移函数本身的行为验证，不是靠事件消费）。
func TestRecordStatusMachine_ACCEPTED到SENT(t *testing.T) {
	r := testRepo(t)
	id := mustCreateRecordTx(t, r, CreateRecordInput{
		RecipientSub: uniqueID("sub"), Category: CategoryWorkflowTask, Channel: ChannelIM,
		Title: "t", Body: "b", SourceComponent: "x", SourceAggregate: "y", SourceID: "1",
	}, StatusPending)

	markTx(t, r, func(ctx context.Context, tx *sql.Tx) error {
		return MarkAcceptedTx(ctx, tx, id, "dingtalk-task-1")
	})
	rec, err := r.GetRecord(context.Background(), RecordIDString(id))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusAccepted || rec.ExternalTaskID != "dingtalk-task-1" {
		t.Fatalf("期望 ACCEPTED + external_task_id，实际 status=%q external_task_id=%q", rec.Status, rec.ExternalTaskID)
	}

	markTx(t, r, func(ctx context.Context, tx *sql.Tx) error {
		return MarkSentTx(ctx, tx, id)
	})
	rec, err = r.GetRecord(context.Background(), RecordIDString(id))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusSent {
		t.Fatalf("期望 SENT，实际 %q", rec.Status)
	}
}

// TestMarkRetryingTx_达到上限前重试_达到上限后终态 验证重试计数与上限
// 判断（consumer.handleRetryableFailureTx 用它决定要不要转
// FAILED_PERMANENT）。
func TestMarkRetryingTx_达到上限前重试_达到上限后终态(t *testing.T) {
	r := testRepo(t)
	id := mustCreateRecordTx(t, r, CreateRecordInput{
		RecipientSub: uniqueID("sub"), Category: CategoryWorkflowTask, Channel: ChannelIM,
		Title: "t", Body: "b", SourceComponent: "x", SourceAggregate: "y", SourceID: "1",
	}, StatusPending)

	var count int32
	markTx(t, r, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		count, err = MarkRetryingTx(ctx, tx, id, "限流")
		return err
	})
	if count != 1 {
		t.Fatalf("期望第一次重试 retry_count=1，实际 %d", count)
	}
	rec, err := r.GetRecord(context.Background(), RecordIDString(id))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusRetrying || rec.RetryCount != 1 {
		t.Fatalf("期望 RETRYING + retry_count=1，实际 status=%q retry_count=%d", rec.Status, rec.RetryCount)
	}

	markTx(t, r, func(ctx context.Context, tx *sql.Tx) error {
		return MarkFailedPermanentTx(ctx, tx, id, "重试次数耗尽")
	})
	rec, err = r.GetRecord(context.Background(), RecordIDString(id))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusFailedPermanent {
		t.Fatalf("期望 FAILED_PERMANENT，实际 %q", rec.Status)
	}

	// 终态之后再收到一次迟到的失败结果——ErrRecordNotActive，不是真的错误。
	markTx(t, r, func(ctx context.Context, tx *sql.Tx) error {
		_, err := MarkRetryingTx(ctx, tx, id, "迟到的失败")
		if !errors.Is(err, ErrRecordNotActive) {
			t.Fatalf("终态记录再收到失败结果，期望 ErrRecordNotActive，实际 %v", err)
		}
		return nil
	})
}

func TestListRecords_owner维过滤(t *testing.T) {
	r := testRepo(t)
	subA := uniqueID("subA")
	subB := uniqueID("subB")
	mustCreateRecordTx(t, r, CreateRecordInput{
		RecipientSub: subA, Category: CategoryWorkflowTask, Channel: ChannelIM,
		Title: "a", Body: "b", SourceComponent: "x", SourceAggregate: "y", SourceID: "1",
	}, StatusPending)
	mustCreateRecordTx(t, r, CreateRecordInput{
		RecipientSub: subB, Category: CategoryWorkflowTask, Channel: ChannelIM,
		Title: "a", Body: "b", SourceComponent: "x", SourceAggregate: "y", SourceID: "2",
	}, StatusPending)

	records, _, err := r.ListRecords(context.Background(), ListInput{RecipientSub: subA, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].RecipientSub != subA {
		t.Fatalf("期望只看到 subA 自己的 1 条记录，实际 %d 条", len(records))
	}
}
