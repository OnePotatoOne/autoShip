// Package handler 实现需求池列表、手动录入、详情抽屉、分拣、相似关联、关闭归档与点“开工”的 HTTP 控制器。
package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"autoship/internal/modules/requirement/repository"
	"autoship/internal/modules/requirement/service"
	"autoship/pkg/response"
)

// Handler 封装管理端需求池 HTTP 接口。
type Handler struct {
	svc *service.Service
}

// New 创建需求池控制器实例。
// 参数 svc 为需求服务；返回 *Handler。
func New(svc *service.Service) *Handler {
	return &Handler{svc: svc}
}

// List 分页查询需求池。
// 参数 c 为 Gin 请求上下文；输出标准分页结果。
func (h *Handler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	productID, _ := strconv.ParseUint(c.Query("productId"), 10, 64)

	list, total, err := h.svc.List(repository.Filter{
		ProductID:  productID,
		ModuleCode: c.Query("moduleCode"),
		IssueType:  c.Query("issueType"),
		Status:     c.Query("status"),
		Keyword:    c.Query("keyword"),
		Page:       page,
		PageSize:   pageSize,
	})
	if err != nil {
		response.ServerError(c, "查询需求池失败")
		return
	}
	response.OKPage(c, total, page, pageSize, list)
}

// Create 手动录入需求。
// 参数 c 为 Gin 请求上下文；输出新建的 Requirement。
func (h *Handler) Create(c *gin.Context) {
	var in service.ManualCreateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.BadRequest(c, "需求参数格式非法")
		return
	}
	actor := c.GetString("autoship.admin_username")
	req, err := h.svc.CreateManual(in, actor)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, req)
}

// Get 获取单条需求完整详情（含对话、原型、相似关联与时间线）。
// 参数 c 为 Gin 请求上下文；输出 DetailBundle。
func (h *Handler) Get(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	bundle, err := h.svc.GetDetail(id)
	if err != nil {
		response.ServerError(c, "读取需求详情失败")
		return
	}
	if bundle == nil {
		response.NotFound(c, "需求不存在")
		return
	}
	response.OK(c, bundle)
}

// Triage 分拣调整或排期确认。
// 参数 c 为 Gin 请求上下文；输出更新后的 Requirement。
func (h *Handler) Triage(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var in service.TriageInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.BadRequest(c, "分拣参数格式非法")
		return
	}
	actor := c.GetString("autoship.admin_username")
	req, err := h.svc.ManualTriage(id, in, actor)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, req)
}

// Link 建立相似需求关联。
// 参数 c 为 Gin 请求上下文；输出成功状态。
func (h *Handler) Link(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		TargetRequirementID uint64 `json:"targetRequirementId"`
		LinkType            string `json:"linkType"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.TargetRequirementID == 0 {
		response.BadRequest(c, "请指定要关联的目标需求 ID")
		return
	}
	actor := c.GetString("autoship.admin_username")
	if err := h.svc.LinkSimilar(id, req.TargetRequirementID, req.LinkType, actor); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, gin.H{"linked": true})
}

// Close 关闭并归档需求。
// 参数 c 为 Gin 请求上下文；输出关闭后的 Requirement。
func (h *Handler) Close(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		Reason string `json:"reason"`
		Note   string `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "关闭参数格式非法")
		return
	}
	actor := c.GetString("autoship.admin_username")
	updated, err := h.svc.Close(id, req.Reason, req.Note, actor)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, updated)
}

// StartWork 点“开工”启动该需求的自动研发任务。
// 参数 c 为 Gin 请求上下文；输出新建的 workItemId。
func (h *Handler) StartWork(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		PlanConfirmRequired *bool `json:"planConfirmRequired"`
		TokenBudget         int   `json:"tokenBudget"`
		MaxTurns            int   `json:"maxTurns"`
		MaxDurationMinutes  int   `json:"maxDurationMinutes"`
	}
	_ = c.ShouldBindJSON(&req)
	planConfirm := true
	if req.PlanConfirmRequired != nil {
		planConfirm = *req.PlanConfirmRequired
	}
	actor := c.GetString("autoship.admin_username")
	workID, err := h.svc.StartWork(id, planConfirm, req.TokenBudget, req.MaxTurns, req.MaxDurationMinutes, actor)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, gin.H{"workItemId": workID})
}
