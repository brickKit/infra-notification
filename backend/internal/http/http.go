// Package http 是 infra-notification 的 REST 面（对外路径前缀
// /infra/notification，与 assembly.yaml 的 edge_routes 一致）。⚠️ 没有
// "发通知"这类接口——本组件唯一入口是事件（contracts/notification.openapi.yaml
// 顶部的注释、设计计划 §3.1）。
package http

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	besdk "github.com/brickKit/be-sdk-go"
	"github.com/brickKit/infra-notification/backend/internal/repo"
	"github.com/brickKit/infra-notification/backend/internal/service"
)

// RegisterRoutes 挂载业务路由——三个权限键均来自 assembly.yaml 的
// permissions 段，一字不差（漏写编译不过，见 besdk.GET/PUT 的签名）。
func RegisterRoutes(eng *gin.Engine, svc *service.Service) {
	g := eng.Group("/infra/notification")
	besdk.GET(g, "/notifications", "infra.notification.view", listMyRecordsHandler(svc))
	besdk.GET(g, "/preferences", "infra.notification.preference.edit", getPreferencesHandler(svc))
	besdk.PUT(g, "/preferences", "infra.notification.preference.edit", setPreferencesHandler(svc))
	besdk.GET(g, "/admin/records", "infra.notification.admin", listAdminRecordsHandler(svc))
}

const rfc3339 = "2006-01-02T15:04:05.999999999Z07:00"

func toRecordDTO(r *repo.NotificationRecord) gin.H {
	return gin.H{
		"id": repo.RecordIDString(r.ID), "recipient_sub": r.RecipientSub,
		"category": r.Category, "channel": r.Channel, "title": r.Title, "body": r.Body,
		"status": r.Status, "external_task_id": r.ExternalTaskID,
		"source_component": r.SourceComponent, "source_aggregate": r.SourceAggregate, "source_id": r.SourceID,
		"retry_count": r.RetryCount,
		"created_at":  r.CreatedAt.Format(rfc3339), "updated_at": r.UpdatedAt.Format(rfc3339),
	}
}

func toPreferencesDTO(p *repo.Preferences) gin.H {
	categoryChannels := make(gin.H, len(p.CategoryChannels))
	for cat, chans := range p.CategoryChannels {
		categoryChannels[cat] = chans
	}
	return gin.H{
		"sub": p.Sub, "global_channels": p.GlobalChannels, "category_channels": categoryChannels,
	}
}

func listInputFromQuery(c *gin.Context) repo.ListInput {
	pageSize, _ := strconv.Atoi(c.Query("page_size"))
	return repo.ListInput{
		Category: c.Query("category"), Status: c.Query("status"),
		Cursor: c.Query("cursor"), PageSize: int32(pageSize),
	}
}

func listMyRecordsHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		records, nextCursor, err := svc.ListMyRecords(c.Request.Context(), listInputFromQuery(c))
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		dtos := make([]gin.H, 0, len(records))
		for _, r := range records {
			dtos = append(dtos, toRecordDTO(r))
		}
		c.JSON(http.StatusOK, gin.H{"records": dtos, "next_cursor": nextCursor})
	}
}

func listAdminRecordsHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		in := listInputFromQuery(c)
		in.RecipientSub = c.Query("recipient_sub")
		records, nextCursor, err := svc.ListRecordsAdmin(c.Request.Context(), in)
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		dtos := make([]gin.H, 0, len(records))
		for _, r := range records {
			dtos = append(dtos, toRecordDTO(r))
		}
		c.JSON(http.StatusOK, gin.H{"records": dtos, "next_cursor": nextCursor})
	}
}

func getPreferencesHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, err := svc.GetPreferences(c.Request.Context())
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		c.JSON(http.StatusOK, toPreferencesDTO(p))
	}
}

type preferencesUpdateRequest struct {
	GlobalChannels   []string            `json:"global_channels"`
	CategoryChannels map[string][]string `json:"category_channels"`
}

func setPreferencesHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req preferencesUpdateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		p, err := svc.SetPreferences(c.Request.Context(), req.GlobalChannels, req.CategoryChannels)
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		c.JSON(http.StatusOK, toPreferencesDTO(p))
	}
}
