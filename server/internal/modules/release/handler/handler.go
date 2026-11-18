// Package handler 实现管理端版本查询、确认部署、一键新建修复任务，开发 Agent 分片续传制品上传，以及更新 Agent Range 下载制品接口。
package handler

import (
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	agentModel "autoship/internal/modules/agent/model"
	artSvc "autoship/internal/modules/artifact/service"
	"autoship/internal/modules/release/service"
	"autoship/pkg/manifest"
	"autoship/pkg/response"
)

// Handler 封装版本与制品相关 HTTP 接口。
type Handler struct {
	svc       *service.Service
	artifacts *artSvc.Service
}

// New 创建版本与制品控制器。
// 参数 svc 为版本服务，artifacts 为制品磁盘服务；返回 *Handler。
func New(svc *service.Service, artifacts *artSvc.Service) *Handler {
	return &Handler{svc: svc, artifacts: artifacts}
}

// List 管理端分页查询版本列表。
// 参数 c 为 Gin 请求上下文；输出标准分页 JSON。
func (h *Handler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	productID, _ := strconv.ParseUint(c.Query("productId"), 10, 64)

	list, total, err := h.svc.List(productID, c.Query("status"), page, pageSize)
	if err != nil {
		response.ServerError(c, "查询版本列表失败")
		return
	}
	response.OKPage(c, total, page, pageSize, list)
}

// Get 管理端获取单个版本聚合详情。
// 参数 c 为 Gin 请求上下文；输出 ReleaseDetailBundle JSON。
func (h *Handler) Get(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	bundle, err := h.svc.GetDetail(id)
	if err != nil {
		response.ServerError(c, "查询版本详情失败")
		return
	}
	if bundle == nil {
		response.NotFound(c, "版本不存在")
		return
	}
	response.OK(c, bundle)
}

// CreateFixWork 对发布失败的版本一键新建关联修复开发任务。
// 参数 c 为 Gin 请求上下文；输出 fixWorkItemId。
func (h *Handler) CreateFixWork(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		Note string `json:"note"`
	}
	_ = c.ShouldBindJSON(&req)
	workID, err := h.svc.CreateFixWorkForRelease(id, req.Note)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, gin.H{"workItemId": workID})
}

// ConfirmDeploy 在后台弹窗确认向目标服务器下发升级指令。
// 参数 c 为 Gin 请求上下文；输出创建的部署记录列表。
func (h *Handler) ConfirmDeploy(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		TargetIDs []uint64 `json:"targetIds"`
		BackupDB  bool     `json:"backupDb"`
	}
	_ = c.ShouldBindJSON(&req)
	actor := c.GetString("autoship.admin_username")
	deps, err := h.svc.ConfirmDeploy(id, req.TargetIDs, req.BackupDB, actor)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, gin.H{"deployments": deps})
}

// InitUpload 处理开发 Agent 或发布工具发起的制品包分片上传初始化（含 Ed25519 清单验签）。
// 参数 c 为 Gin 请求上下文；输出 releaseId 与 uploadId。
func (h *Handler) InitUpload(c *gin.Context) {
	releaseID, _ := strconv.ParseUint(c.Param("releaseId"), 10, 64)
	var req struct {
		TotalSize int64                    `json:"totalSize"`
		SHA256    string                   `json:"sha256"`
		Manifest  manifest.ReleaseManifest `json:"manifest"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "初始化分片上传参数格式非法")
		return
	}
	actualReleaseID, uploadID, err := h.svc.InitBundleUpload(releaseID, req.TotalSize, req.SHA256, req.Manifest)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, gin.H{
		"releaseId": actualReleaseID,
		"uploadId":  uploadID,
	})
}

// UploadChunk 接收单个二进制分片流。
// 参数 c 为 Gin 请求上下文；输出 receivedBytes。
func (h *Handler) UploadChunk(c *gin.Context) {
	releaseID, _ := strconv.ParseUint(c.Param("releaseId"), 10, 64)
	uploadID := c.Param("uploadId")
	index, err := strconv.Atoi(c.Param("index"))
	if err != nil {
		response.BadRequest(c, "分片序号非法")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32*1024*1024)
	data, err := io.ReadAll(c.Request.Body)
	if err != nil {
		response.BadRequest(c, "读取分片字节流失败")
		return
	}
	n, err := h.svc.UploadChunk(releaseID, uploadID, index, data)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, gin.H{"receivedBytes": n})
}

// CompleteUpload 合并分片并将版本置为待部署。
// 参数 c 为 Gin 请求上下文；输出更新后的 Release。
func (h *Handler) CompleteUpload(c *gin.Context) {
	releaseID, _ := strconv.ParseUint(c.Param("releaseId"), 10, 64)
	uploadID := c.Param("uploadId")
	var req struct {
		CIRunURL   string `json:"ciRunUrl"`
		ProjectMap string `json:"projectMap"`
	}
	_ = c.ShouldBindJSON(&req)
	rel, err := h.svc.CompleteBundleUpload(releaseID, uploadID, req.CIRunURL, req.ProjectMap)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, rel)
}

// ReportPublishFailed 接收开发 Agent 上报的发布阶段失败状态。
// 参数 c 为 Gin 请求上下文；输出更新后的 Release。
func (h *Handler) ReportPublishFailed(c *gin.Context) {
	releaseID, _ := strconv.ParseUint(c.Param("releaseId"), 10, 64)
	var req struct {
		TagPushed   bool   `json:"tagPushed"`
		HasConflict bool   `json:"hasConflict"`
		CIRunURL    string `json:"ciRunUrl"`
		Reason      string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "失败上报参数格式非法")
		return
	}
	rel, err := h.svc.ReportPublishFailure(releaseID, req.TagPushed, req.HasConflict, req.CIRunURL, req.Reason)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, rel)
}

// DownloadArtifactForUpdateAgent 供已鉴权更新 Agent 按所属产品权限范围执行断点续传制品包下载。
// 参数 c 为 Gin 请求上下文；直接输出文件二进制或 HTTP 206 分段流。
func (h *Handler) DownloadArtifactForUpdateAgent(c *gin.Context) {
	v, ok := c.Get("autoship.agent")
	if !ok {
		response.Unauthorized(c, "未认证的更新 Agent")
		return
	}
	ag, _ := v.(*agentModel.Agent)
	releaseID, _ := strconv.ParseUint(c.Param("releaseId"), 10, 64)
	art, err := h.svc.GetArtifactForTarget(releaseID, ag.ProductID)
	if err != nil {
		response.Forbidden(c, err.Error())
		return
	}
	if err := h.artifacts.ServeFileWithRange(c.Writer, c.Request, art.StoragePath, art.FileName); err != nil {
		response.NotFound(c, err.Error())
	}
}
