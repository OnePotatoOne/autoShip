// Package handler 实现产品档案、模块表、范围规则、权限档案一键放行及产品知识的管理端 HTTP 接口。
package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"autoship/internal/modules/product/model"
	"autoship/internal/modules/product/service"
	"autoship/pkg/response"
)

// Handler 封装产品档案相关管理接口。
type Handler struct {
	svc *service.Service
}

// New 创建产品控制器实例。
// 参数 svc 为产品业务服务；返回 *Handler。
func New(svc *service.Service) *Handler {
	return &Handler{svc: svc}
}

// List 获取全部产品档案列表。
// 参数 c 为 Gin 请求上下文；输出产品数组 JSON。
func (h *Handler) List(c *gin.Context) {
	list, err := h.svc.List()
	if err != nil {
		response.ServerError(c, "查询产品列表失败")
		return
	}
	response.OK(c, list)
}

// Get 获取单个产品的聚合详情（含模块、范围规则、权限档案、知识库）。
// 参数 c 为 Gin 请求上下文；输出 ProductFullBundle JSON。
func (h *Handler) Get(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	bundle, err := h.svc.GetFullBundle(id)
	if err != nil {
		response.ServerError(c, "查询产品详情失败")
		return
	}
	if bundle == nil {
		response.NotFound(c, "产品不存在")
		return
	}
	response.OK(c, bundle)
}

// Create 新建产品档案。
// 参数 c 为 Gin 请求上下文；输出新建的 Product 对象。
func (h *Handler) Create(c *gin.Context) {
	var in model.Product
	if err := c.ShouldBindJSON(&in); err != nil {
		response.BadRequest(c, "产品参数格式非法")
		return
	}
	if err := h.svc.Create(&in); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, in)
}

// Update 更新产品主档案配置。
// 参数 c 为 Gin 请求上下文；输出更新后的 Product 对象。
func (h *Handler) Update(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var in model.Product
	if err := c.ShouldBindJSON(&in); err != nil {
		response.BadRequest(c, "产品参数格式非法")
		return
	}
	updated, err := h.svc.Update(id, in)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, updated)
}

// Delete 删除产品档案。
// 参数 c 为 Gin 请求上下文；输出删除成功标志。
func (h *Handler) Delete(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.svc.Delete(id); err != nil {
		response.ServerError(c, "删除产品失败")
		return
	}
	response.OK(c, gin.H{"deleted": true})
}

// UpdateModules 批量保存某产品的业务模块划分。
// 参数 c 为 Gin 请求上下文；输出更新后的模块列表。
func (h *Handler) UpdateModules(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		Modules []model.ProductModule `json:"modules"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "模块列表格式非法")
		return
	}
	if err := h.svc.SaveModules(id, req.Modules); err != nil {
		response.ServerError(c, "保存产品模块失败")
		return
	}
	bundle, _ := h.svc.GetFullBundle(id)
	response.OK(c, bundle)
}

// UpdateScopeRules 批量保存某产品的公共区与受保护路径规则。
// 参数 c 为 Gin 请求上下文；输出更新后的聚合档案。
func (h *Handler) UpdateScopeRules(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		Rules []model.ProductScopeRule `json:"rules"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "范围规则格式非法")
		return
	}
	if err := h.svc.SaveScopeRules(id, req.Rules); err != nil {
		response.ServerError(c, "保存范围规则失败")
		return
	}
	bundle, _ := h.svc.GetFullBundle(id)
	response.OK(c, bundle)
}

// UpdatePermissionProfile 更新产品的沙箱权限档案配置。
// 参数 c 为 Gin 请求上下文；输出更新后的 ProductPermissionProfile。
func (h *Handler) UpdatePermissionProfile(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		Mode                 string   `json:"mode"`
		AllowedWritePaths    []string `json:"allowedWritePaths"`
		ExtraDeniedReadPaths []string `json:"extraDeniedReadPaths"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "权限档案参数非法")
		return
	}
	prof, err := h.svc.UpdatePermissionProfile(id, req.Mode, req.AllowedWritePaths, req.ExtraDeniedReadPaths)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, prof)
}

// AllowPath 一键将开发 Agent 被拦截的路径加入该产品的沙箱允许清单。
// 参数 c 为 Gin 请求上下文；输出更新后的 ProductPermissionProfile。
func (h *Handler) AllowPath(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		Path string `json:"path"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "放行路径参数非法")
		return
	}
	prof, err := h.svc.AllowBlockedPath(id, req.Path)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, prof)
}

// UpdateKnowledge 批量更新某产品的人工产品知识库。
// 参数 c 为 Gin 请求上下文；输出更新后的聚合档案。
func (h *Handler) UpdateKnowledge(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		Items []model.ProductKnowledge `json:"items"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "产品知识参数格式非法")
		return
	}
	if err := h.svc.SaveKnowledge(id, req.Items); err != nil {
		response.ServerError(c, "保存产品知识失败")
		return
	}
	bundle, _ := h.svc.GetFullBundle(id)
	response.OK(c, bundle)
}
