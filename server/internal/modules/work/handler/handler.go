// Package handler 实现管理后台开发任务列表、详情、方案确认、答复提问及验收通过/打回/放弃接口。
package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"autoship/internal/modules/work/service"
	"autoship/pkg/response"
)

// Handler 封装管理后台开发任务 HTTP 接口。
type Handler struct {
	svc *service.Service
}

// New 创建开发任务控制器实例。
// 参数 svc 为开发任务服务；返回 *Handler。
func New(svc *service.Service) *Handler {
	return &Handler{svc: svc}
}

// List 分页查询开发任务。
// 参数 c 为 Gin 请求上下文；输出标准分页 JSON。
func (h *Handler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	productID, _ := strconv.ParseUint(c.Query("productId"), 10, 64)

	list, total, err := h.svc.List(productID, c.Query("status"), page, pageSize)
	if err != nil {
		response.ServerError(c, "查询开发任务失败")
		return
	}
	response.OKPage(c, total, page, pageSize, list)
}

// Get 获取单个开发任务完整详情（含门禁、改动范围三区明细、活动流、预览地址与验收包）。
// 参数 c 为 Gin 请求上下文；输出 WorkDetailBundle JSON。
func (h *Handler) Get(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	bundle, err := h.svc.GetDetail(id)
	if err != nil {
		response.ServerError(c, "查询开发任务详情失败")
		return
	}
	if bundle == nil {
		response.NotFound(c, "开发任务不存在")
		return
	}
	response.OK(c, bundle)
}

// ConfirmPlan 确认或驳回开发任务的一页纸方案。
// 参数 c 为 Gin 请求上下文；输出更新后的开发任务。
func (h *Handler) ConfirmPlan(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		Approved bool   `json:"approved"`
		Comment  string `json:"comment"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "方案确认参数非法")
		return
	}
	item, err := h.svc.ConfirmPlan(id, req.Approved, req.Comment)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, item)
}

// Answer 回答开发 Agent 的 ask_owner 提问。
// 参数 c 为 Gin 请求上下文；输出更新后的开发任务。
func (h *Handler) Answer(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		QuestionID string `json:"questionId"`
		Answer     string `json:"answer"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Answer == "" {
		response.BadRequest(c, "请填写答复内容")
		return
	}
	item, err := h.svc.AnswerQuestion(id, req.QuestionID, req.Answer)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, item)
}

// Accept 验收通过开发任务并触发版本发布流水线。
// 参数 c 为 Gin 请求上下文；输出 workItem 与生成的 releaseId。
func (h *Handler) Accept(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		Version                     string `json:"version"`
		ApproveDestructiveMigration bool   `json:"approveDestructiveMigration"`
		Comment                     string `json:"comment"`
	}
	_ = c.ShouldBindJSON(&req)
	actor := c.GetString("autoship.admin_username")
	item, releaseID, err := h.svc.Accept(id, req.Version, req.ApproveDestructiveMigration, req.Comment, actor)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, gin.H{
		"workItem":  item,
		"releaseId": releaseID,
	})
}

// Reject 验收打回开发任务。
// 参数 c 为 Gin 请求上下文；输出更新后的开发任务。
func (h *Handler) Reject(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		Comment string `json:"comment"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "打回参数非法")
		return
	}
	item, err := h.svc.Reject(id, req.Comment)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, item)
}

// Abandon 放弃当前开发任务并将关联需求退回待排期。
// 参数 c 为 Gin 请求上下文；输出更新后的开发任务。
func (h *Handler) Abandon(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		Comment string `json:"comment"`
	}
	_ = c.ShouldBindJSON(&req)
	actor := c.GetString("autoship.admin_username")
	item, err := h.svc.Abandon(id, req.Comment, actor)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, item)
}
