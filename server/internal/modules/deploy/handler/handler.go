// Package handler 实现管理后台部署目标管理、部署分步日志查询、手动回滚、手动数据库备份，以及更新 Agent 结果回传接口。
package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"

	agentModel "autoship/internal/modules/agent/model"
	"autoship/internal/modules/deploy/model"
	"autoship/internal/modules/deploy/service"
	"autoship/pkg/response"
)

// Handler 封装部署中心管理端与 Agent 端 HTTP 控制器。
type Handler struct {
	svc *service.Service
}

// New 创建部署模块控制器实例。
// 参数 svc 为部署服务；返回 *Handler。
func New(svc *service.Service) *Handler {
	return &Handler{svc: svc}
}

// ListTargets 查询部署目标列表。
// 参数 c 为 Gin 请求上下文；输出 DeployTarget 数组。
func (h *Handler) ListTargets(c *gin.Context) {
	productID, _ := strconv.ParseUint(c.Query("productId"), 10, 64)
	list, err := h.svc.ListTargets(productID)
	if err != nil {
		response.ServerError(c, "查询部署目标失败")
		return
	}
	response.OK(c, list)
}

// CreateTarget 新建部署目标。
// 参数 c 为 Gin 请求上下文；输出新建的 DeployTarget。
func (h *Handler) CreateTarget(c *gin.Context) {
	var in model.DeployTarget
	if err := c.ShouldBindJSON(&in); err != nil {
		response.BadRequest(c, "部署目标参数格式非法")
		return
	}
	if err := h.svc.CreateTarget(&in); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, in)
}

// UpdateTarget 更新部署目标配置。
// 参数 c 为 Gin 请求上下文；输出更新后的 DeployTarget。
func (h *Handler) UpdateTarget(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var in model.DeployTarget
	if err := c.ShouldBindJSON(&in); err != nil {
		response.BadRequest(c, "部署目标参数格式非法")
		return
	}
	updated, err := h.svc.UpdateTarget(id, in)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, updated)
}

// DeleteTarget 删除部署目标。
// 参数 c 为 Gin 请求上下文；输出 deleted=true。
func (h *Handler) DeleteTarget(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.svc.DeleteTarget(id); err != nil {
		response.ServerError(c, "删除部署目标失败")
		return
	}
	response.OK(c, gin.H{"deleted": true})
}

// ListDeployments 分页查询部署执行记录。
// 参数 c 为 Gin 请求上下文；输出标准分页 JSON。
func (h *Handler) ListDeployments(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	productID, _ := strconv.ParseUint(c.Query("productId"), 10, 64)
	targetID, _ := strconv.ParseUint(c.Query("targetId"), 10, 64)

	list, total, err := h.svc.ListDeployments(productID, targetID, c.Query("status"), page, pageSize)
	if err != nil {
		response.ServerError(c, "查询部署记录失败")
		return
	}
	response.OKPage(c, total, page, pageSize, list)
}

// GetDeployment 获取单次部署详情、分步进度与数据库备份。
// 参数 c 为 Gin 请求上下文；输出 DeploymentDetailBundle。
func (h *Handler) GetDeployment(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	bundle, err := h.svc.GetDeploymentDetail(id)
	if err != nil {
		response.ServerError(c, "查询部署详情失败")
		return
	}
	if bundle == nil {
		response.NotFound(c, "部署记录不存在")
		return
	}
	response.OK(c, bundle)
}

// Rollback 手动触发按部署 ID 回滚。
// 参数 c 为 Gin 请求上下文；输出部署对象。
func (h *Handler) Rollback(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&req)
	actor := c.GetString("autoship.admin_username")
	dep, err := h.svc.TriggerManualRollback(id, req.Reason, actor)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, dep)
}

// BackupTarget 手动触发某部署目标立即备份数据库。
// 参数 c 为 Gin 请求上下文；输出 backupUid。
func (h *Handler) BackupTarget(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	uid, err := h.svc.TriggerManualBackup(id)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, gin.H{"backupUid": uid})
}

// ListBackups 查询数据库备份列表及恢复提示。
// 参数 c 为 Gin 请求上下文；输出备份记录数组。
func (h *Handler) ListBackups(c *gin.Context) {
	productID, _ := strconv.ParseUint(c.Query("productId"), 10, 64)
	targetID, _ := strconv.ParseUint(c.Query("targetId"), 10, 64)
	list, err := h.svc.ListBackups(productID, targetID)
	if err != nil {
		response.ServerError(c, "查询数据库备份列表失败")
		return
	}
	response.OK(c, list)
}

// ReportFromUpdateAgent 处理公网更新 Agent 提交的部署分步状态与终态结果报告。
// 参数 c 为 Gin 请求上下文；输出 ok=true。
func (h *Handler) ReportFromUpdateAgent(c *gin.Context) {
	v, ok := c.Get("autoship.agent")
	if !ok {
		response.Unauthorized(c, "未认证的更新 Agent")
		return
	}
	ag, _ := v.(*agentModel.Agent)
	depUID := c.Param("deploymentId")

	var in service.AgentReportInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.BadRequest(c, "部署进度上报参数格式非法")
		return
	}
	if err := h.svc.HandleAgentReport(ag.DeployTargetID, ag.ProductID, depUID, in); err != nil {
		response.Forbidden(c, err.Error())
		return
	}
	response.OK(c, gin.H{"ok": true})
}
