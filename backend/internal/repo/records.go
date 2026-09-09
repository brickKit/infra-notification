package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	besdk "github.com/brickKit/be-sdk-go"
)

// 通道与状态常量——字符串字面量与 contracts 里的 proto enum、REST 契约
// 逐字对应（migrations/001_create_notification.up.sql 的 CHECK 约束只
// 管 status，channel 没有 DB 层约束，这里的常量就是唯一的权威口径）。
const (
	ChannelIM    = "IM"
	ChannelEmail = "EMAIL"
	ChannelSMS   = "SMS"

	StatusPending         = "PENDING"
	StatusAccepted        = "ACCEPTED"
	StatusSent            = "SENT"
	StatusFailedPermanent = "FAILED_PERMANENT"
	StatusSuppressed      = "SUPPRESSED"
	StatusRetrying        = "RETRYING"
)

// ValidChannel 供 service 层做入参校验（SetPreferences 的
// global_channels/category_channels 不接受未知通道名）。
func ValidChannel(c string) bool {
	return c == ChannelIM || c == ChannelEmail || c == ChannelSMS
}

// ErrRecordNotActive：对一条已经是终态（SENT/FAILED_PERMANENT/SUPPRESSED）
// 的记录再收到一次投递结果事件——同 infra-workflow ErrNotPending 的判据，
// 是"晚到的重复通知"，不是系统错误。调用方（consumer）据此判断要不要
// 继续走重试/重发逻辑。
var ErrRecordNotActive = errors.New("通知记录已经不是活跃状态")

type NotificationRecord struct {
	ID              int64
	RecipientSub    string
	Category        string
	Channel         string
	Title           string
	Body            string
	Status          string
	ExternalTaskID  string
	SourceComponent string
	SourceAggregate string
	SourceID        string
	RetryCount      int32
	LastError       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type CreateRecordInput struct {
	RecipientSub    string
	Category        string
	Channel         string
	Title           string
	Body            string
	SourceComponent string
	SourceAggregate string
	SourceID        string
}

func RecordIDString(id int64) string { return strconv.FormatInt(id, 10) }

// ParseRecordID 导出给 consumer 包用（integration.im.result.v1 携带的
// record_id 是字符串，落库前要转回 BIGINT）。
func ParseRecordID(s string) (int64, error) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: record_id 不合法：%q", ErrInvalidArgument, s)
	}
	return id, nil
}

func parseRecordID(s string) (int64, error) { return ParseRecordID(s) }

const recordSelectColumns = `SELECT id, recipient_sub, category, channel, title, body, status,
	external_task_id, source_component, source_aggregate, source_id, retry_count, last_error,
	created_at, updated_at`

func scanRecordRow(row *sql.Row, r *NotificationRecord) error {
	return row.Scan(&r.ID, &r.RecipientSub, &r.Category, &r.Channel, &r.Title, &r.Body, &r.Status,
		&r.ExternalTaskID, &r.SourceComponent, &r.SourceAggregate, &r.SourceID, &r.RetryCount, &r.LastError,
		&r.CreatedAt, &r.UpdatedAt)
}

func scanRecordRows(rows *sql.Rows, r *NotificationRecord) error {
	return rows.Scan(&r.ID, &r.RecipientSub, &r.Category, &r.Channel, &r.Title, &r.Body, &r.Status,
		&r.ExternalTaskID, &r.SourceComponent, &r.SourceAggregate, &r.SourceID, &r.RetryCount, &r.LastError,
		&r.CreatedAt, &r.UpdatedAt)
}

// CreateRecordTx 在调用方已有的事务里插入一条通知记录，返回自增 id。
// ⚠️ 只供事件消费者使用（besdk.Consume 给的 tx 里，不能再开
// besdk.WithTx——那是另一个独立会话，同 erp-finance
// PostSalesOrderEntryTx 顶部注释的既有判据）。
func CreateRecordTx(ctx context.Context, tx *sql.Tx, in CreateRecordInput, status string) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO notification_records
			(recipient_sub, category, channel, title, body, status,
			 source_component, source_aggregate, source_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id`,
		in.RecipientSub, in.Category, in.Channel, in.Title, in.Body, status,
		in.SourceComponent, in.SourceAggregate, in.SourceID,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("写 notification_records: %w", err)
	}
	return id, nil
}

// GetRecordTx 供同一事务内的后续步骤（比如重试时要拿回 title/body/channel
// 重新组装 dispatch payload）使用。
func GetRecordTx(ctx context.Context, tx *sql.Tx, recordID int64) (*NotificationRecord, error) {
	var r NotificationRecord
	err := scanRecordRow(tx.QueryRowContext(ctx, recordSelectColumns+` FROM notification_records WHERE id = $1`, recordID), &r)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: record id=%d", ErrNotFound, recordID)
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// GetRecord 按 record_id 查详情——不做数据权限过滤（同 infra-workflow
// GetTask 的既有判据：repo 层只管"这行存不存在"，调用方决定"该不该被
// 这个人看见"）。
func (r *Repo) GetRecord(ctx context.Context, recordID string) (*NotificationRecord, error) {
	id, err := parseRecordID(recordID)
	if err != nil {
		return nil, err
	}
	var rec NotificationRecord
	err = besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		return scanRecordRow(tx.QueryRowContext(ctx, recordSelectColumns+` FROM notification_records WHERE id = $1`, id), &rec)
	})
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: record id=%s", ErrNotFound, recordID)
	}
	if err != nil {
		return nil, wrap("查通知记录", err)
	}
	return &rec, nil
}

// BatchGetRecords 是防 N+1 的唯一合法批量读方式（§3.8）。查不到的 id
// 直接在结果里省略，不报错（同 batchGet 惯例）。
func (r *Repo) BatchGetRecords(ctx context.Context, recordIDs []string) ([]*NotificationRecord, error) {
	if len(recordIDs) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(recordIDs))
	for _, s := range recordIDs {
		id, err := parseRecordID(s)
		if err != nil {
			continue // 不合法的 id 当"查不到"处理，不报错
		}
		ids = append(ids, id)
	}
	var out []*NotificationRecord
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, recordSelectColumns+` FROM notification_records WHERE id = ANY($1::bigint[])`, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rec NotificationRecord
			if err := scanRecordRows(rows, &rec); err != nil {
				return err
			}
			out = append(out, &rec)
		}
		return rows.Err()
	})
	return out, wrap("批量查通知记录", err)
}

// ListInput 是 ListRecords 的查询参数。⚠️ 与 infra-workflow 的两维
// ScopeFilter 不同——本组件只有 owner 一维（assembly.yaml data_scopes），
// 所以没有 OR/toggle 的问题：AdminView=false 时 RecipientSub 强制等于
// 调用者自己（由 service 层从 besdk.ScopeOf(ctx).Owner 填入），
// AdminView=true 时 RecipientSub 是可选的精确过滤（排障场景，管理员可能
// 不带这个参数看全部，也可能带上只看某个人的）。
type ListInput struct {
	RecipientSub string // AdminView=false 时必填且等于调用者自己；AdminView=true 时可选
	Category     string // 空 = 不筛
	Status       string // 空 = 不筛
	AdminView    bool
	Cursor       string
	PageSize     int32
}

func (r *Repo) ListRecords(ctx context.Context, in ListInput) ([]*NotificationRecord, string, error) {
	pageSize := in.PageSize
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	var afterID int64
	if in.Cursor != "" {
		id, err := parseRecordID(in.Cursor)
		if err != nil {
			return nil, "", err
		}
		afterID = id
	}

	query := recordSelectColumns + ` FROM notification_records WHERE id > $1`
	args := []any{afterID}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if in.RecipientSub != "" {
		query += ` AND recipient_sub = ` + arg(in.RecipientSub)
	}
	if in.Category != "" {
		query += ` AND category = ` + arg(in.Category)
	}
	if in.Status != "" {
		query += ` AND status = ` + arg(in.Status)
	}
	query += ` ORDER BY id LIMIT ` + arg(int64(pageSize)+1)

	var out []*NotificationRecord
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rec NotificationRecord
			if err := scanRecordRows(rows, &rec); err != nil {
				return err
			}
			out = append(out, &rec)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", wrap("列通知记录", err)
	}

	nextCursor := ""
	if int32(len(out)) > pageSize {
		out = out[:pageSize]
		nextCursor = RecordIDString(out[len(out)-1].ID)
	}
	return out, nextCursor, nil
}

// ── 状态流转：PENDING → ACCEPTED → SENT/FAILED_PERMANENT/RETRYING，
// 全部由消费 integration.im.result.v1 驱动（设计计划 §2、§4）。────────

// MarkAcceptedTx：适配器已受理（asyncsend_v2 返回了 task_id），不代表
// 送达。⚠️ 只允许从 PENDING/RETRYING 转入——已经是终态的记录收到一条
// 迟到的 ACCEPTED 是重复/乱序事件，静默忽略（同 infra-workflow
// ErrNotPending 判据的反面：这里不报错是因为 ACCEPTED 本身就是中间态，
// 迟到的中间态通知不影响已经算数的终态）。
func MarkAcceptedTx(ctx context.Context, tx *sql.Tx, recordID int64, externalTaskID string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE notification_records
		SET status = 'ACCEPTED', external_task_id = $2, updated_at = now(), version = version + 1
		WHERE id = $1 AND status IN ('PENDING', 'RETRYING')`, recordID, externalTaskID)
	return err
}

// MarkSentTx：phase=CONFIRMED 且 success=true。终态。
func MarkSentTx(ctx context.Context, tx *sql.Tx, recordID int64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE notification_records
		SET status = 'SENT', updated_at = now(), version = version + 1
		WHERE id = $1 AND status IN ('PENDING', 'ACCEPTED', 'RETRYING')`, recordID)
	return err
}

// MarkFailedPermanentTx：适配器判定不可重试，或重试次数耗尽。终态。
func MarkFailedPermanentTx(ctx context.Context, tx *sql.Tx, recordID int64, reason string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE notification_records
		SET status = 'FAILED_PERMANENT', last_error = $2, updated_at = now(), version = version + 1
		WHERE id = $1 AND status NOT IN ('SENT', 'FAILED_PERMANENT', 'SUPPRESSED')`, recordID, reason)
	return err
}

// MarkRetryingTx：phase=CONFIRMED 且 success=false 且 retryable=true。
// retry_count 原子加一并返回新值——调用方（consumer）拿它跟重试上限比较，
// 决定是重新发一次 dispatch 事件还是转 FAILED_PERMANENT。
//
// ⚠️ 0 行命中时返回 ErrRecordNotActive，不是 sql.ErrNoRows 直接透传——
// 这种情况只可能是"记录已经是终态了，又收到一条迟到的失败结果"，调用方
// 应该把它当成正常的乱序事件安静跳过，不应该报错也不应该重发。
func MarkRetryingTx(ctx context.Context, tx *sql.Tx, recordID int64, reason string) (int32, error) {
	var newCount int32
	err := tx.QueryRowContext(ctx, `
		UPDATE notification_records
		SET status = 'RETRYING', last_error = $2, retry_count = retry_count + 1,
		    updated_at = now(), version = version + 1
		WHERE id = $1 AND status NOT IN ('SENT', 'FAILED_PERMANENT', 'SUPPRESSED')
		RETURNING retry_count`, recordID, reason).Scan(&newCount)
	if err == sql.ErrNoRows {
		return 0, ErrRecordNotActive
	}
	return newCount, err
}
