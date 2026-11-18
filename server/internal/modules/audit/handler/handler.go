// Package handler 实现审计日志检索、系统全局设置读写及根 CA 证书下载的 HTTP 控制器。
package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"autoship/internal/modules/audit/repository"
	"autoship/internal/modules/audit/service"
	"autoship/pkg/pki"
	"autoship/pkg/response"
)

// Handler 封装审计模块与全局设置模块的 Gin 接口处理方法。
type Handler struct {
	svc *service.Service
	ca  *pki.Bundle
}

// New 创建审计与设置 HTTP 处理器。
// 参数 svc 为审计服务实例，ca 为 PKI 根证书 Bundle；返回 *Handler。
func New(svc *service.Service, ca *pki.Bundle) *Handler {
	return &Handler{svc: svc, ca: ca}
}

// ListAuditLogs 分页查询平台全量操作审计记录。
// 参数 c 为 Gin 请求上下文；响应标准分页 JSON。
func (h *Handler) ListAuditLogs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	productID, _ := strconv.ParseUint(c.Query("productId"), 10, 64)

	list, total, err := h.svc.List(repository.ListFilter{
		ProductID: productID,
		Module:    c.Query("module"),
		Action:    c.Query("action"),
		ActorID:   c.Query("actor"),
		Page:      page,
		PageSize:  pageSize,
	})
	if err != nil {
		response.ServerError(c, "查询审计日志失败")
		return
	}
	response.OKPage(c, total, page, pageSize, list)
}

// GetSettings 获取当前平台的模型通道、飞书通知与 PKI 指纹设置。
// 参数 c 为 Gin 请求上下文；返回脱敏后的配置视图。
func (h *Handler) GetSettings(c *gin.Context) {
	fp := ""
	if h.ca != nil {
		fp = h.ca.CAFingerprint
	}
	response.OK(c, h.svc.GetSettingsView(fp))
}

// UpdateSettings 修改全局模型通道、飞书通知与制品保留策略。
// 参数 c 为 Gin 请求上下文；返回更新后的配置视图。
func (h *Handler) UpdateSettings(c *gin.Context) {
	var in service.UpdateSettingsInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.BadRequest(c, "请求参数格式非法")
		return
	}
	actor := c.GetString("autoship.admin_username")
	if err := h.svc.UpdateSettings(in, actor); err != nil {
		response.ServerError(c, "保存系统设置失败")
		return
	}
	fp := ""
	if h.ca != nil {
		fp = h.ca.CAFingerprint
	}
	response.OK(c, h.svc.GetSettingsView(fp))
}

// DownloadCACert 下载 autoShip 自建根 CA 公开证书（供管理员电脑/手机安装系统信任）。
// 参数 c 为 Gin 请求上下文；直接返回 application/x-x509-ca-cert PEM 文件流。
func (h *Handler) DownloadCACert(c *gin.Context) {
	if h.ca == nil || len(h.ca.CACertPEM) == 0 {
		response.NotFound(c, "根 CA 证书尚未生成")
		return
	}
	c.Header("Content-Disposition", `attachment; filename="autoship-ca.crt"`)
	c.Data(http.StatusOK, "application/x-x509-ca-cert", h.ca.CACertPEM)
}
