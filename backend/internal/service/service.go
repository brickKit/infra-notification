// Package service 是 infra-notification 的业务规则层：入参校验 + 给
// http/grpc 一个不依赖 repo 内部细节的稳定入口（同 infra-workflow 的
// 既有判据）。数据权限的可见性/归属校验也在这一层做——本组件只有 owner
// 一维（recipient_sub 等值），没有 infra-workflow 那种两维 OR/toggle 的
// 复杂度。
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	besdk "github.com/brickKit/be-sdk-go"
	"github.com/brickKit/infra-notification/backend/internal/repo"
)

var ErrInvalidArgument = errors.New("参数不合法")

type Service struct {
	repo   *repo.Repo
	logger *slog.Logger
}

func New(r *repo.Repo, logger *slog.Logger) *Service {
	return &Service{repo: r, logger: logger}
}

// ── REST 面：走 besdk.ScopeOf(ctx)（人类操作，有已验签 Claims）──

// ListMyRecords 是 GET /infra/notification/notifications："我的通知
// 历史"——recipient_sub 强制等于调用者自己，不接受越权查询（同
// data_scopes 声明：owner 维是 equals，不是前缀/OR，本组件没有"部门主管
// 看得到下属通知"这种口子）。
func (s *Service) ListMyRecords(ctx context.Context, in repo.ListInput) ([]*repo.NotificationRecord, string, error) {
	in.AdminView = false
	in.RecipientSub = besdk.ScopeOf(ctx).Owner
	return s.repo.ListRecords(ctx, in)
}

// ListRecordsAdmin 是 GET /infra/notification/admin/records：排障视图，
// 绕过 owner 维（in.RecipientSub 若由 http 层的查询参数填好，原样透传做
// 精确过滤；留空就是看全部）。
func (s *Service) ListRecordsAdmin(ctx context.Context, in repo.ListInput) ([]*repo.NotificationRecord, string, error) {
	in.AdminView = true
	return s.repo.ListRecords(ctx, in)
}

func (s *Service) GetPreferences(ctx context.Context) (*repo.Preferences, error) {
	sub := besdk.ScopeOf(ctx).Owner
	return s.repo.GetPreferences(ctx, sub)
}

func (s *Service) SetPreferences(ctx context.Context, globalChannels []string, categoryChannels map[string][]string) (*repo.Preferences, error) {
	sub := besdk.ScopeOf(ctx).Owner
	if err := validateChannels(globalChannels); err != nil {
		return nil, err
	}
	for cat, chans := range categoryChannels {
		if err := validateChannels(chans); err != nil {
			return nil, fmt.Errorf("分类 %q: %w", cat, err)
		}
	}
	return s.repo.SetPreferences(ctx, sub, globalChannels, categoryChannels)
}

func validateChannels(channels []string) error {
	for _, c := range channels {
		if !repo.ValidChannel(c) {
			return fmt.Errorf("%w: 未知通道 %q", ErrInvalidArgument, c)
		}
	}
	return nil
}

// ── gRPC 面：组件间协议，本项目目前没有任何组件转发/验证 JWT，不做数据
// 权限过滤（同 infra-workflow ListTasks/BatchGetTasks 的既有判据）──

func (s *Service) BatchGetRecords(ctx context.Context, recordIDs []string) ([]*repo.NotificationRecord, error) {
	return s.repo.BatchGetRecords(ctx, recordIDs)
}

func (s *Service) ListRecords(ctx context.Context, in repo.ListInput) ([]*repo.NotificationRecord, string, error) {
	in.AdminView = true // 借用"不强制 RecipientSub"这条分支；组件间协议按调用方传入的过滤条件查
	return s.repo.ListRecords(ctx, in)
}

func (s *Service) GetPreferencesFor(ctx context.Context, sub string) (*repo.Preferences, error) {
	if sub == "" {
		return nil, fmt.Errorf("%w: sub 不能为空", ErrInvalidArgument)
	}
	return s.repo.GetPreferences(ctx, sub)
}

func (s *Service) SetPreferencesFor(ctx context.Context, sub string, globalChannels []string, categoryChannels map[string][]string) (*repo.Preferences, error) {
	if sub == "" {
		return nil, fmt.Errorf("%w: sub 不能为空", ErrInvalidArgument)
	}
	if err := validateChannels(globalChannels); err != nil {
		return nil, err
	}
	for cat, chans := range categoryChannels {
		if err := validateChannels(chans); err != nil {
			return nil, fmt.Errorf("分类 %q: %w", cat, err)
		}
	}
	return s.repo.SetPreferences(ctx, sub, globalChannels, categoryChannels)
}
