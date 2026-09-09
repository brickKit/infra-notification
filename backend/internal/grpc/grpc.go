// Package grpc 实现 infra.notification.v1.NotificationService——内部
// gRPC 面（§2.1）。HTTP 与 gRPC 共用同一个 service.Service，业务逻辑
// 只写一遍（同 infra-workflow/erp-inventory 的既有判据）。
package grpc

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	notificationv1 "github.com/brickKit/infra-notification/gen/infra/notification/v1"

	"github.com/brickKit/infra-notification/backend/internal/repo"
	"github.com/brickKit/infra-notification/backend/internal/service"
)

type server struct {
	notificationv1.UnimplementedNotificationServiceServer
	svc *service.Service
}

// New 构造 gRPC 服务端实现。module.go 用它注册到 grpc.Server。
func New(svc *service.Service) notificationv1.NotificationServiceServer {
	return &server{svc: svc}
}

func toProtoChannel(c string) notificationv1.Channel {
	switch c {
	case repo.ChannelIM:
		return notificationv1.Channel_CHANNEL_IM
	case repo.ChannelEmail:
		return notificationv1.Channel_CHANNEL_EMAIL
	case repo.ChannelSMS:
		return notificationv1.Channel_CHANNEL_SMS
	default:
		return notificationv1.Channel_CHANNEL_UNSPECIFIED
	}
}

func fromProtoChannel(c notificationv1.Channel) string {
	switch c {
	case notificationv1.Channel_CHANNEL_IM:
		return repo.ChannelIM
	case notificationv1.Channel_CHANNEL_EMAIL:
		return repo.ChannelEmail
	case notificationv1.Channel_CHANNEL_SMS:
		return repo.ChannelSMS
	default:
		return ""
	}
}

func toProtoStatus(s string) notificationv1.RecordStatus {
	switch s {
	case repo.StatusPending:
		return notificationv1.RecordStatus_RECORD_STATUS_PENDING
	case repo.StatusAccepted:
		return notificationv1.RecordStatus_RECORD_STATUS_ACCEPTED
	case repo.StatusSent:
		return notificationv1.RecordStatus_RECORD_STATUS_SENT
	case repo.StatusFailedPermanent:
		return notificationv1.RecordStatus_RECORD_STATUS_FAILED_PERMANENT
	case repo.StatusSuppressed:
		return notificationv1.RecordStatus_RECORD_STATUS_SUPPRESSED
	case repo.StatusRetrying:
		return notificationv1.RecordStatus_RECORD_STATUS_RETRYING
	default:
		// ⚠️ UNSPECIFIED 就是 NOT_FOUND 的信号，不是"忘了填"（同
		// infra-workflow toProtoStatus 的既有判据）。
		return notificationv1.RecordStatus_RECORD_STATUS_UNSPECIFIED
	}
}

func fromProtoStatus(s notificationv1.RecordStatus) string {
	switch s {
	case notificationv1.RecordStatus_RECORD_STATUS_PENDING:
		return repo.StatusPending
	case notificationv1.RecordStatus_RECORD_STATUS_ACCEPTED:
		return repo.StatusAccepted
	case notificationv1.RecordStatus_RECORD_STATUS_SENT:
		return repo.StatusSent
	case notificationv1.RecordStatus_RECORD_STATUS_FAILED_PERMANENT:
		return repo.StatusFailedPermanent
	case notificationv1.RecordStatus_RECORD_STATUS_SUPPRESSED:
		return repo.StatusSuppressed
	case notificationv1.RecordStatus_RECORD_STATUS_RETRYING:
		return repo.StatusRetrying
	default:
		return ""
	}
}

func toProtoRecord(r *repo.NotificationRecord) *notificationv1.NotificationRecord {
	if r == nil {
		return nil
	}
	return &notificationv1.NotificationRecord{
		Id: repo.RecordIDString(r.ID), RecipientSub: r.RecipientSub,
		Category: r.Category, Channel: toProtoChannel(r.Channel),
		Title: r.Title, Body: r.Body, Status: toProtoStatus(r.Status),
		ExternalTaskId:  r.ExternalTaskID,
		SourceComponent: r.SourceComponent, SourceAggregate: r.SourceAggregate, SourceId: r.SourceID,
		RetryCount: r.RetryCount,
		CreatedAt:  timestamppb.New(r.CreatedAt), UpdatedAt: timestamppb.New(r.UpdatedAt),
	}
}

func toProtoChannelSet(channels []string) *notificationv1.ChannelSet {
	out := make([]notificationv1.Channel, 0, len(channels))
	for _, c := range channels {
		out = append(out, toProtoChannel(c))
	}
	return &notificationv1.ChannelSet{Channels: out}
}

func fromProtoChannelSet(cs *notificationv1.ChannelSet) []string {
	if cs == nil {
		return nil
	}
	out := make([]string, 0, len(cs.Channels))
	for _, c := range cs.Channels {
		out = append(out, fromProtoChannel(c))
	}
	return out
}

func toProtoPreferences(p *repo.Preferences) *notificationv1.Preferences {
	globalChannels := make([]notificationv1.Channel, 0, len(p.GlobalChannels))
	for _, c := range p.GlobalChannels {
		globalChannels = append(globalChannels, toProtoChannel(c))
	}
	categoryChannels := make(map[string]*notificationv1.ChannelSet, len(p.CategoryChannels))
	for cat, chans := range p.CategoryChannels {
		categoryChannels[cat] = toProtoChannelSet(chans)
	}
	return &notificationv1.Preferences{
		Sub: p.Sub, GlobalChannels: globalChannels, CategoryChannels: categoryChannels,
	}
}

func fromProtoCategoryChannels(m map[string]*notificationv1.ChannelSet) map[string][]string {
	out := make(map[string][]string, len(m))
	for cat, cs := range m {
		out[cat] = fromProtoChannelSet(cs)
	}
	return out
}

func (s *server) BatchGetRecords(ctx context.Context, req *notificationv1.BatchGetRecordsRequest) (*notificationv1.BatchGetRecordsResponse, error) {
	records, err := s.svc.BatchGetRecords(ctx, req.RecordIds)
	if err != nil {
		return nil, service.ToStatus(err)
	}
	out := make([]*notificationv1.NotificationRecord, 0, len(records))
	for _, r := range records {
		out = append(out, toProtoRecord(r))
	}
	return &notificationv1.BatchGetRecordsResponse{Records: out}, nil
}

func (s *server) ListRecords(ctx context.Context, req *notificationv1.ListRecordsRequest) (*notificationv1.ListRecordsResponse, error) {
	records, nextCursor, err := s.svc.ListRecords(ctx, repo.ListInput{
		Category: req.Category, Status: fromProtoStatus(req.Status),
		Cursor: req.Cursor, PageSize: req.PageSize,
	})
	if err != nil {
		return nil, service.ToStatus(err)
	}
	out := make([]*notificationv1.NotificationRecord, 0, len(records))
	for _, r := range records {
		out = append(out, toProtoRecord(r))
	}
	return &notificationv1.ListRecordsResponse{Records: out, NextCursor: nextCursor}, nil
}

func (s *server) GetPreferences(ctx context.Context, req *notificationv1.GetPreferencesRequest) (*notificationv1.Preferences, error) {
	p, err := s.svc.GetPreferencesFor(ctx, req.Sub)
	if err != nil {
		return nil, service.ToStatus(err)
	}
	return toProtoPreferences(p), nil
}

func (s *server) SetPreferences(ctx context.Context, req *notificationv1.SetPreferencesRequest) (*notificationv1.SetPreferencesResponse, error) {
	globalChannels := make([]string, 0, len(req.GlobalChannels))
	for _, c := range req.GlobalChannels {
		globalChannels = append(globalChannels, fromProtoChannel(c))
	}
	p, err := s.svc.SetPreferencesFor(ctx, req.Sub, globalChannels, fromProtoCategoryChannels(req.CategoryChannels))
	if err != nil {
		return nil, service.ToStatus(err)
	}
	return &notificationv1.SetPreferencesResponse{Preferences: toProtoPreferences(p)}, nil
}
