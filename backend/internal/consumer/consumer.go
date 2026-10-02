// Package consumer 是 infra-notification 唯一的业务入口：没有 Notify 这类
// 同步 rpc，谁要通知，谁发自己的领域事件，本组件来消费（同步接口会逼调用方
// 建依赖边，而"通知发不出去"不该让业务动作失败）。每条 subject 跑在自己的
// goroutine 里（besdk.Consume 是阻塞到 ctx 取消才返回的循环，互不影响）：
//   - infra.workflow.task.created.v1（infra-workflow） → 目前唯一的
//     通知来源：查偏好、建 notification_records、发 dispatch 事件
//   - infra.iam.user.created.v1/.updated.v1/.disabled.v1（infra-iam-casdoor）
//     → 维护 user_contacts 快照
//   - integration.im.result.v1（任一 channel:im 适配器） → 按 phase
//     推进 notification_records 的状态机，失败且 retryable 才重试
package consumer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"

	besdk "github.com/brickKit/be-sdk-go"
	"github.com/nats-io/nats.go"

	"github.com/brickKit/infra-notification/v2/backend/internal/repo"
)

// maxRetries：phase=CONFIRMED 且 retryable=true 时最多重试这么多次才转
// FAILED_PERMANENT。立即重发、不做指数退避：IM 平台的限流需不需要退避
// 曲线，要有真实的限流数据才看得出来，没有已知需求支撑的复杂度是提前抽象。
const maxRetries = 3

// Start 每条 subject 起一个 goroutine。targetAdapters 是发 dispatch.im.v1
// 事件时点名哪些适配器处理，来自配置键 IM_TARGET_ADAPTERS（module.go），
// 不是硬编码在这里——换掉 / 新增 IM 适配器只改配置，不改代码。
func Start(ctx context.Context, db *sql.DB, role, schema string, nc *nats.Conn, logger *slog.Logger, targetAdapters []string) error {
	subjects := []struct {
		subject string
		handle  func(context.Context, *sql.Tx, besdk.Event) error
	}{
		{"infra.workflow.task.created.v1", workflowTaskCreatedHandler(schema, targetAdapters, logger)},
		{"infra.iam.user.created.v1", userUpsertHandler()},
		{"infra.iam.user.updated.v1", userUpsertHandler()},
		{"infra.iam.user.disabled.v1", userDisabledHandler()},
		{"integration.im.result.v1", imResultHandler(schema, targetAdapters, logger)},
	}

	errCh := make(chan error, len(subjects))
	for _, s := range subjects {
		s := s
		go func() {
			errCh <- besdk.Consume(ctx, nc, db, role, schema, s.subject, s.handle)
		}()
	}

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err // ⚠️ 返回 error，不许 log.Fatal：外壳里一个成员退进程，所有成员一起下线
	}
}

// ── infra.workflow.task.created.v1：目前唯一的通知来源 ────────────────

// workflowTaskPayload 字段照抄 infra-workflow 发布的事件契约
// （它的 contracts/events 里 infra.workflow.task.created.v1 的 payload）。
type workflowTaskPayload struct {
	TaskID          string `json:"task_id"`
	AssigneeSub     string `json:"assignee_sub"`
	Title           string `json:"title"`
	Type            string `json:"type"`
	SourceComponent string `json:"source_component"`
	SourceAggregate string `json:"source_aggregate"`
	SourceID        string `json:"source_id"`
}

func workflowTaskCreatedHandler(schema string, targetAdapters []string, logger *slog.Logger) func(context.Context, *sql.Tx, besdk.Event) error {
	return func(ctx context.Context, tx *sql.Tx, ev besdk.Event) error {
		var p workflowTaskPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return fmt.Errorf("解析 %s payload: %w", ev.Subject, err)
		}

		contact, err := repo.GetContactTx(ctx, tx, p.AssigneeSub)
		if err != nil {
			return fmt.Errorf("查收件人联系方式: %w", err)
		}

		channels, err := repo.ResolvePreferredChannelsTx(ctx, tx, p.AssigneeSub, repo.CategoryWorkflowTask)
		if err != nil {
			return err
		}
		// ⚠️ 停用用户覆盖 critical 兜底——workflow_task 是 critical，
		// ResolvePreferredChannelsTx 保证正常情况下不会解出空集，但"这个
		// 人已经离职/停用"是另一层判断（user_contacts.disabled：人走了还
		// 继续收审批通知是外部可见的错），两层判断不合并（见 preferences.go
		// ResolvePreferredChannelsTx 顶部注释），这里显式再判一次。
		if contact.Disabled {
			channels = nil
		}

		body := fmt.Sprintf("您有一条新的待办事项待处理：%s", p.Title)

		if len(channels) == 0 {
			// 全部通道都被挡掉（目前只有"用户被停用"这一条路径能让
			// critical 类别也解出空集，见上）。仍然落一条记录，用 IM 作为
			// 展示用的通道值——它没有实际投递意义，只是为了在
			// GET /admin/records 排障视图上有一行可查的"为什么没发出去"
			// （同 SUPPRESSED 状态本身的存在意义：安静地不建任何记录，
			// 会让"通知真的发不出去了"和"系统正常但没触发"两种情况无法
			// 区分）。
			_, err := repo.CreateRecordTx(ctx, tx, repo.CreateRecordInput{
				RecipientSub: p.AssigneeSub, Category: repo.CategoryWorkflowTask, Channel: repo.ChannelIM,
				Title: p.Title, Body: body,
				SourceComponent: p.SourceComponent, SourceAggregate: p.SourceAggregate, SourceID: p.SourceID,
			}, repo.StatusSuppressed)
			return err
		}

		for _, ch := range channels {
			recordID, err := repo.CreateRecordTx(ctx, tx, repo.CreateRecordInput{
				RecipientSub: p.AssigneeSub, Category: repo.CategoryWorkflowTask, Channel: ch,
				Title: p.Title, Body: body,
				SourceComponent: p.SourceComponent, SourceAggregate: p.SourceAggregate, SourceID: p.SourceID,
			}, repo.StatusPending)
			if err != nil {
				return err
			}

			switch ch {
			case repo.ChannelIM:
				if contact.Phone == "" {
					// 记 FAILED_PERMANENT 而不是 SUPPRESSED：proto
					// RecordStatus 把 SUPPRESSED 定义为"被用户偏好挡掉"，
					// 手机号缺失是数据 / 集成缺口，不是用户的选择，语义上
					// 是"投递失败且不可重试"。记录留着，排障视图里看得到原因。
					if err := repo.MarkFailedPermanentTx(ctx, tx, recordID, "收件人手机号为空，无法投递"); err != nil {
						return err
					}
					continue
				}
				if err := publishIMDispatchTx(tx, schema, recordID, 1, targetAdapters, contact.Phone, p.Title, body); err != nil {
					return err
				}
			case repo.ChannelEmail:
				// 目前没有邮件适配器——发了 dispatch 事件但没人消费，记录
				// 会一直停在 PENDING。默认全局通道只有 IM（preferences.go
				// defaultGlobalChannels），只有用户自己打开 EMAIL 才会走到这里。
				if contact.Email == "" {
					if err := repo.MarkFailedPermanentTx(ctx, tx, recordID, "收件人邮箱为空，无法投递"); err != nil {
						return err
					}
					continue
				}
				if err := publishEmailDispatchTx(tx, schema, recordID, 1, targetAdapters, contact.Email, p.Title, body); err != nil {
					return err
				}
			default:
				logger.Warn("未知通道，跳过 dispatch", "channel", ch, "record_id", repo.RecordIDString(recordID))
			}
		}
		return nil
	}
}

// publishIMDispatchTx/publishEmailDispatchTx 的 attempt 参数必须是这条
// record_id 第几次尝试（初次派发是 1，第一次重试是 2，以此类推），并且
// 原样当 Event.Version 用。
//
// ⚠️ 这不是"顺手记一下第几次"——dispatch.im.v1 的 aggregate_id 固定是
// record_id，同一个 record_id 的初次派发与之后每一次重试派发都共用
// 这一个 subject+aggregate_id。event_inbox 的去重规则是"同一
// (subject, aggregate_id) 只接受版本严格递增的事件，version 不比已见过
// 的最大值大就静默跳过"（be-sdk-go events.go 的 handleOne）——如果重试
// 时仍然用 Version:1，adapter 那侧的 event_inbox 会认为这是"已经处理过
// 的重复消息"直接吞掉，症状是"重试次数在 notification_records 里涨了，
// 钉钉那边却什么都没收到"，而且没有任何报错。
func publishIMDispatchTx(tx *sql.Tx, schema string, recordID int64, attempt int, targetAdapters []string, phone, title, body string) error {
	payload, err := json.Marshal(map[string]any{
		"record_id": repo.RecordIDString(recordID), "attempt": attempt, "target_adapters": targetAdapters,
		"recipient_phone": phone, "title": title, "body": body,
	})
	if err != nil {
		return err
	}
	return besdk.PublishOutbox(tx, schema, besdk.Event{
		Subject: "infra.notification.dispatch.im.v1", AggregateID: repo.RecordIDString(recordID),
		Version: int64(attempt), Payload: payload,
	})
}

func publishEmailDispatchTx(tx *sql.Tx, schema string, recordID int64, attempt int, targetAdapters []string, email, title, body string) error {
	payload, err := json.Marshal(map[string]any{
		"record_id": repo.RecordIDString(recordID), "attempt": attempt, "target_adapters": targetAdapters,
		"recipient_email": email, "title": title, "body": body,
	})
	if err != nil {
		return err
	}
	return besdk.PublishOutbox(tx, schema, besdk.Event{
		Subject: "infra.notification.dispatch.email.v1", AggregateID: repo.RecordIDString(recordID),
		Version: int64(attempt), Payload: payload,
	})
}

// ── infra.iam.user.{created,updated,disabled}.v1：user_contacts 快照 ──

type userPayload struct {
	Sub         string `json:"sub"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
}

func userUpsertHandler() func(context.Context, *sql.Tx, besdk.Event) error {
	return func(ctx context.Context, tx *sql.Tx, ev besdk.Event) error {
		var p userPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return fmt.Errorf("解析 %s payload: %w", ev.Subject, err)
		}
		return repo.UpsertContactTx(ctx, tx, p.Sub, p.DisplayName, p.Email, p.Phone, ev.Version)
	}
}

type userDisabledPayload struct {
	Sub string `json:"sub"`
}

func userDisabledHandler() func(context.Context, *sql.Tx, besdk.Event) error {
	return func(ctx context.Context, tx *sql.Tx, ev besdk.Event) error {
		var p userDisabledPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return fmt.Errorf("解析 %s payload: %w", ev.Subject, err)
		}
		return repo.SetContactDisabledTx(ctx, tx, p.Sub, ev.Version)
	}
}

// ── integration.im.result.v1：族级投递结果，phase 区分受理/确认 ────────

type imResultPayload struct {
	RecordID       string `json:"record_id"`
	Adapter        string `json:"adapter"`
	Phase          string `json:"phase"` // ACCEPTED / CONFIRMED
	Success        bool   `json:"success"`
	ErrorCode      string `json:"error_code"`
	Retryable      bool   `json:"retryable"`
	ExternalTaskID string `json:"external_task_id"`
}

const (
	phaseAccepted  = "ACCEPTED"
	phaseConfirmed = "CONFIRMED"
)

func imResultHandler(schema string, targetAdapters []string, logger *slog.Logger) func(context.Context, *sql.Tx, besdk.Event) error {
	return func(ctx context.Context, tx *sql.Tx, ev besdk.Event) error {
		var p imResultPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return fmt.Errorf("解析 %s payload: %w", ev.Subject, err)
		}
		recordID, err := parseRecordIDOrWarn(p.RecordID, logger)
		if err != nil {
			return nil // 不合法的 record_id 不值得让整个消费循环重跑，记警告后跳过
		}

		switch p.Phase {
		case phaseAccepted:
			return repo.MarkAcceptedTx(ctx, tx, recordID, p.ExternalTaskID)

		case phaseConfirmed:
			if p.Success {
				if err := repo.MarkSentTx(ctx, tx, recordID); err != nil {
					return err
				}
				return publishSentTx(tx, schema, p.RecordID)
			}
			if !p.Retryable {
				if err := repo.MarkFailedPermanentTx(ctx, tx, recordID, p.ErrorCode); err != nil {
					return err
				}
				return publishFailedTx(tx, schema, p.RecordID, p.ErrorCode)
			}
			return handleRetryableFailureTx(ctx, tx, schema, recordID, p, targetAdapters)

		default:
			logger.Warn("未知 phase，忽略", "phase", p.Phase, "record_id", p.RecordID)
			return nil
		}
	}
}

func parseRecordIDOrWarn(s string, logger *slog.Logger) (int64, error) {
	id, err := repo.ParseRecordID(s)
	if err != nil {
		logger.Warn("integration.im.result.v1 携带的 record_id 不合法，跳过", "record_id", s)
		return 0, err
	}
	return id, nil
}

// handleRetryableFailureTx：phase=CONFIRMED、success=false、retryable=true。
// retry_count 达到上限前重新发一次 dispatch.im.v1（立即重发，不做退避，
// 见 maxRetries 注释）；达到上限转 FAILED_PERMANENT。
//
// ⚠️ 只处理 IM 通道的重发——integration.im.result.v1 是 IM 族的结果事件，
// 只有 IM 记录会收到它，重发自然也只需要重建 IM 的 dispatch payload。
// EMAIL / SMS 有了适配器和各自的结果事件时，再按 record.Channel 分支扩展。
func handleRetryableFailureTx(ctx context.Context, tx *sql.Tx, schema string, recordID int64, p imResultPayload, targetAdapters []string) error {
	newCount, err := repo.MarkRetryingTx(ctx, tx, recordID, p.ErrorCode)
	if err != nil {
		if err == repo.ErrRecordNotActive {
			return nil // 记录已经是终态，是迟到的重复失败结果，安静跳过
		}
		return err
	}
	if newCount > maxRetries {
		if err := repo.MarkFailedPermanentTx(ctx, tx, recordID, "重试次数耗尽: "+p.ErrorCode); err != nil {
			return err
		}
		return publishFailedTx(tx, schema, p.RecordID, p.ErrorCode)
	}

	rec, err := repo.GetRecordTx(ctx, tx, recordID)
	if err != nil {
		return err
	}
	contact, err := repo.GetContactTx(ctx, tx, rec.RecipientSub)
	if err != nil {
		return err
	}
	if contact.Phone == "" {
		return repo.MarkFailedPermanentTx(ctx, tx, recordID, "重试时收件人手机号为空，无法投递")
	}
	// attempt = retry_count + 1：初次派发是 attempt 1（retry_count 当时是
	// 0），第一次重试时 newCount 已经被 MarkRetryingTx 加到 1，对应
	// attempt 2，以此类推——同 publishIMDispatchTx 顶部注释里 event_inbox
	// 严格递增 version 的要求。
	return publishIMDispatchTx(tx, schema, recordID, int(newCount)+1, targetAdapters, contact.Phone, rec.Title, rec.Body)
}

func publishSentTx(tx *sql.Tx, schema, recordID string) error {
	payload, err := json.Marshal(map[string]any{"record_id": recordID, "channel": repo.ChannelIM})
	if err != nil {
		return err
	}
	return besdk.PublishOutbox(tx, schema, besdk.Event{
		Subject: "infra.notification.sent.v1", AggregateID: recordID, Version: 1, Payload: payload,
	})
}

func publishFailedTx(tx *sql.Tx, schema, recordID, errMsg string) error {
	payload, err := json.Marshal(map[string]any{"record_id": recordID, "channel": repo.ChannelIM, "error": errMsg})
	if err != nil {
		return err
	}
	return besdk.PublishOutbox(tx, schema, besdk.Event{
		Subject: "infra.notification.failed.v1", AggregateID: recordID, Version: 1, Payload: payload,
	})
}
