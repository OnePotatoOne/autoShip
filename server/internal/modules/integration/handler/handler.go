// Package handler 实现管理后台接入方 CRUD 及一次性密钥展示接口。
package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"autoship/internal/modules/integration/service"
	"autoship/pkg/response"
)

// Handler 封装接入方管理 HTTP 接口方法。
type Handler struct {
	svc *service.Service
}

// New 创建接入方控制器实例。
// 参数 svc 为接入方服务；返回 *Handler。
func New(svc *service.Service) *Handler {
	return &Handler{svc: svc}
}

// List 查询所有接入方列表。
// 参数 c 为 Gin 请求上下文；输出接入方数组 JSON。
func (h *Handler) List(c *gin.Context) {
	list, err := h.svc.List()
	if err != nil {
		response.ServerError(c, "查询接入方列表失败")
		return
	}
	response.OK(c, list)
}

// Create 创建新的业务接入方并一次性返回明文 AppSecret。
// 参数 c 为 Gin 请求上下文；输出包含 integration 与 plainAppSecret 的 JSON。
func (h *Handler) Create(c *gin.Context) {
	var in service.CreateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.BadRequest(c, "接入方参数格式非法")
		return
	}
	item, plainSecret, err := h.svc.Create(in)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, gin.H{
		"integration":    item,
		"plainAppSecret": plainSecret,
	})
}

// Update 更新已有接入方的白名单、频率配额及绑定产品。
// 参数 c 为 Gin 请求上下文；输出更新后的接入方对象。
func (h *Handler) Update(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var in service.CreateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.BadRequest(c, "接入方参数格式非法")
		return
	}
	updated, err := h.svc.Update(id, in)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, updated)
}

// Delete 删除指定接入方。
// 参数 c 为 Gin 请求上下文；输出删除成功响应。
func (h *Handler) Delete(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.svc.Delete(id); err != nil {
		response.ServerError(c, "删除接入方失败")
		return
	}
	response.OK(c, gin.H{"deleted": true})
}
