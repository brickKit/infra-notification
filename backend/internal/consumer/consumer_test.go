package consumer

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/nats-io/nats.go"

	"github.com/brickKit/infra-notification/backend/internal/repo"
)

const (
	testRole   = "infra_notification_rw"
	testSchema = "infra_notification"
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

func natsURLForTest(t *testing.T) string {
	t.Helper()
	if u := os.Getenv("TEST_NATS_URL"); u != "" {
		return u
	}
	return nats.DefaultURL
}

// publishEvent 的 aggregateID 必须每次调用都不同——event_inbox 按
// (subject, aggregate_id, version) 做单调去重且持久化，同 erp-finance
// consumer_test.go 顶部注释的既有教训。
func publishEvent(t *testing.T, nc *nats.Conn, subject, aggregateID string, version int64, payload string) {
	t.Helper()
	msg := &nats.Msg{Subject: subject, Data: []byte(payload), Header: nats.Header{}}
	msg.Header.Set("X-Aggregate-Id", aggregateID)
	msg.Header.Set("X-Version", strconv.FormatInt(version, 10))
	msg.Header.Set("X-Hop-Count", "0")
	if err := nc.PublishMsg(msg); err != nil {
		t.Fatal(err)
	}
}

// runConsumeFor 起一个 besdk.Consume goroutine，跑够 wait 时长后取消并
// 等它退出——同 erp-finance consumer_test.go 的既有节奏（真订阅、真发布、
// 真等待，不直接调 handler 函数）。
func runConsumeFor(t *testing.T, db *sql.DB, nc *nats.Conn, subject string,
	handle func(context.Context, *sql.Tx, besdk.Event) error, publish func(*nats.Conn)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = besdk.Consume(ctx, nc, db, testRole, testSchema, subject, handle)
		close(done)
	}()
	time.Sleep(150 * time.Millisecond)
	publish(nc)
	nc.Flush()
	time.Sleep(400 * time.Millisecond)
	cancel()
	<-done
}

func outboxCount(t *testing.T, db *sql.DB, subject, aggregateID string) int {
	t.Helper()
	var n int
	err := besdk.WithTx(context.Background(), db, testRole, testSchema, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT count(*) FROM event_outbox WHERE subject = $1 AND aggregate_id = $2`,
			subject, aggregateID).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func getRecordByRecipient(t *testing.T, db *sql.DB, sub string) *repo.NotificationRecord {
	t.Helper()
	r := repo.New(db, testRole, testSchema)
	records, _, err := r.ListRecords(context.Background(), repo.ListInput{RecipientSub: sub, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("期望恰好 1 条通知记录，实际 %d 条", len(records))
	}
	return records[0]
}

// TestWorkflowTaskCreatedHandler_建记录并发IM派发事件 是本阶段唯一通知
// 来源的端到端验证：真订阅 infra.workflow.task.created.v1、真发布、
// 真等待，断言落一条 PENDING 记录 + 一条 dispatch.im.v1 outbox 事件，
// target_adapters 用的是传进来的配置（不是硬编码）。
func TestWorkflowTaskCreatedHandler_建记录并发IM派发事件(t *testing.T) {
	db := testDB(t)
	nc, err := nats.Connect(natsURLForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	sub := fmt.Sprintf("consumer-sub-%d", time.Now().UnixNano())
	taskID := fmt.Sprintf("consumer-task-%d", time.Now().UnixNano())

	// 先给这个 sub 一条联系方式快照，否则 IM 通道会因为手机号为空直接
	// 转 FAILED_PERMANENT（见 workflowTaskCreatedHandler 对 contact.Phone
	// 为空的处理）。
	if err := besdk.WithTx(context.Background(), db, testRole, testSchema, func(tx *sql.Tx) error {
		return repo.UpsertContactTx(context.Background(), tx, sub, "张三", "zhangsan@example.com", "13800000000", 1)
	}); err != nil {
		t.Fatal(err)
	}

	runConsumeFor(t, db, nc, "infra.workflow.task.created.v1",
		workflowTaskCreatedHandler(testSchema, []string{"dingtalk"}, slog.Default()),
		func(nc *nats.Conn) {
			payload := fmt.Sprintf(`{"task_id":%q,"assignee_sub":%q,"title":"审批：测试单","type":"APPROVAL","source_component":"infra/workflow","source_aggregate":"workflow_task","source_id":%q}`,
				taskID, sub, taskID)
			publishEvent(t, nc, "infra.workflow.task.created.v1", taskID, 1, payload)
		})

	rec := getRecordByRecipient(t, db, sub)
	if rec.Status != repo.StatusPending {
		t.Fatalf("期望 PENDING，实际 %q", rec.Status)
	}
	if rec.Channel != repo.ChannelIM {
		t.Fatalf("期望 IM 通道，实际 %q", rec.Channel)
	}
	if outboxCount(t, db, "infra.notification.dispatch.im.v1", repo.RecordIDString(rec.ID)) != 1 {
		t.Fatal("期望落 1 条 infra.notification.dispatch.im.v1")
	}
}

// TestWorkflowTaskCreatedHandler_停用用户覆盖critical兜底 验证设计计划
// §4 明文要求的一条：user.disabled.v1 必须消费，停用用户即使类别是
// critical 也不该再收到通知——记录仍然要落一条 SUPPRESSED（排障可见），
// 但不该发 dispatch 事件（没有 IM 消息真的发出去）。
func TestWorkflowTaskCreatedHandler_停用用户覆盖critical兜底(t *testing.T) {
	db := testDB(t)
	nc, err := nats.Connect(natsURLForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	sub := fmt.Sprintf("consumer-disabled-%d", time.Now().UnixNano())
	taskID := fmt.Sprintf("consumer-task-%d", time.Now().UnixNano())

	if err := besdk.WithTx(context.Background(), db, testRole, testSchema, func(tx *sql.Tx) error {
		if err := repo.UpsertContactTx(context.Background(), tx, sub, "李四", "", "13900000000", 1); err != nil {
			return err
		}
		return repo.SetContactDisabledTx(context.Background(), tx, sub, 2)
	}); err != nil {
		t.Fatal(err)
	}

	runConsumeFor(t, db, nc, "infra.workflow.task.created.v1",
		workflowTaskCreatedHandler(testSchema, []string{"dingtalk"}, slog.Default()),
		func(nc *nats.Conn) {
			payload := fmt.Sprintf(`{"task_id":%q,"assignee_sub":%q,"title":"审批：测试单2","type":"APPROVAL","source_component":"infra/workflow","source_aggregate":"workflow_task","source_id":%q}`,
				taskID, sub, taskID)
			publishEvent(t, nc, "infra.workflow.task.created.v1", taskID, 1, payload)
		})

	rec := getRecordByRecipient(t, db, sub)
	if rec.Status != repo.StatusSuppressed {
		t.Fatalf("期望 SUPPRESSED，实际 %q", rec.Status)
	}
	if outboxCount(t, db, "infra.notification.dispatch.im.v1", repo.RecordIDString(rec.ID)) != 0 {
		t.Fatal("停用用户不该发出 dispatch 事件")
	}
}

// TestImResultHandler_ACCEPTED然后CONFIRMED成功 验证状态机 PENDING →
// ACCEPTED → SENT，并且成功时发 infra.notification.sent.v1（设计计划
// §2、§4）。
func TestImResultHandler_ACCEPTED然后CONFIRMED成功(t *testing.T) {
	db := testDB(t)
	nc, err := nats.Connect(natsURLForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	r := repo.New(db, testRole, testSchema)
	var recordID int64
	if err := besdk.WithTx(context.Background(), db, testRole, testSchema, func(tx *sql.Tx) error {
		var err error
		recordID, err = repo.CreateRecordTx(context.Background(), tx, repo.CreateRecordInput{
			RecipientSub: fmt.Sprintf("im-result-sub-%d", time.Now().UnixNano()),
			Category:     repo.CategoryWorkflowTask, Channel: repo.ChannelIM,
			Title: "t", Body: "b", SourceComponent: "x", SourceAggregate: "y", SourceID: "1",
		}, repo.StatusPending)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	recordIDStr := repo.RecordIDString(recordID)

	runConsumeFor(t, db, nc, "integration.im.result.v1",
		imResultHandler(testSchema, []string{"dingtalk"}, slog.Default()),
		func(nc *nats.Conn) {
			payload := fmt.Sprintf(`{"record_id":%q,"adapter":"dingtalk","phase":"ACCEPTED","external_task_id":"dt-task-1"}`, recordIDStr)
			publishEvent(t, nc, "integration.im.result.v1", "accepted-"+recordIDStr, 1, payload)
		})
	rec, err := r.GetRecord(context.Background(), recordIDStr)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != repo.StatusAccepted || rec.ExternalTaskID != "dt-task-1" {
		t.Fatalf("期望 ACCEPTED + external_task_id=dt-task-1，实际 status=%q external_task_id=%q", rec.Status, rec.ExternalTaskID)
	}

	runConsumeFor(t, db, nc, "integration.im.result.v1",
		imResultHandler(testSchema, []string{"dingtalk"}, slog.Default()),
		func(nc *nats.Conn) {
			payload := fmt.Sprintf(`{"record_id":%q,"adapter":"dingtalk","phase":"CONFIRMED","success":true}`, recordIDStr)
			publishEvent(t, nc, "integration.im.result.v1", "confirmed-"+recordIDStr, 2, payload)
		})
	rec, err = r.GetRecord(context.Background(), recordIDStr)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != repo.StatusSent {
		t.Fatalf("期望 SENT，实际 %q", rec.Status)
	}
	if outboxCount(t, db, "infra.notification.sent.v1", recordIDStr) != 1 {
		t.Fatal("期望落 1 条 infra.notification.sent.v1")
	}
}

// TestImResultHandler_CONFIRMED失败且retryable时重新派发 验证
// handleRetryableFailureTx：转 RETRYING、retry_count 加一，并重新发一条
// dispatch.im.v1（立即重发，见 consumer.go maxRetries 注释）。
func TestImResultHandler_CONFIRMED失败且retryable时重新派发(t *testing.T) {
	db := testDB(t)
	nc, err := nats.Connect(natsURLForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	sub := fmt.Sprintf("retry-sub-%d", time.Now().UnixNano())
	if err := besdk.WithTx(context.Background(), db, testRole, testSchema, func(tx *sql.Tx) error {
		return repo.UpsertContactTx(context.Background(), tx, sub, "王五", "", "13700000000", 1)
	}); err != nil {
		t.Fatal(err)
	}

	r := repo.New(db, testRole, testSchema)
	var recordID int64
	if err := besdk.WithTx(context.Background(), db, testRole, testSchema, func(tx *sql.Tx) error {
		var err error
		recordID, err = repo.CreateRecordTx(context.Background(), tx, repo.CreateRecordInput{
			RecipientSub: sub, Category: repo.CategoryWorkflowTask, Channel: repo.ChannelIM,
			Title: "重试测试", Body: "body-x", SourceComponent: "x", SourceAggregate: "y", SourceID: "1",
		}, repo.StatusAccepted)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	recordIDStr := repo.RecordIDString(recordID)

	runConsumeFor(t, db, nc, "integration.im.result.v1",
		imResultHandler(testSchema, []string{"dingtalk"}, slog.Default()),
		func(nc *nats.Conn) {
			payload := fmt.Sprintf(`{"record_id":%q,"adapter":"dingtalk","phase":"CONFIRMED","success":false,"retryable":true,"error_code":"RATE_LIMITED"}`, recordIDStr)
			publishEvent(t, nc, "integration.im.result.v1", "retry-"+recordIDStr, 1, payload)
		})

	rec, err := r.GetRecord(context.Background(), recordIDStr)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != repo.StatusRetrying || rec.RetryCount != 1 {
		t.Fatalf("期望 RETRYING + retry_count=1，实际 status=%q retry_count=%d", rec.Status, rec.RetryCount)
	}
	if outboxCount(t, db, "infra.notification.dispatch.im.v1", recordIDStr) != 1 {
		t.Fatal("期望重新发一条 dispatch.im.v1 用于重试")
	}
}
