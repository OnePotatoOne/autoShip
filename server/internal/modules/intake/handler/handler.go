// Package handler 实现对外标准 Open API（`/open/v1/*`）的全部 7 个接口（含 SSE 流式对话与增量事件拉取，对应 5.5 节）。
package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"autoship/internal/modules/intake/service"
	integModel "autoship/internal/modules/integration/model"
	integSvc "autoship/internal/modules/integration/service"
	reqSvc "autoship/internal/modules/requirement/service"
	"autoship/pkg/response"
)

// Handler 封装 Open API v1 控制器。
type Handler struct {
	intakeSvc *service.Service
	integSvc  *integSvc.Service
	reqSvc    *reqSvc.Service
}

// New 创建 Open API 控制器实例。
// 参数 intakeSvc 为澄清服务，integSvc 为接入方服务，reqSvc 为需求服务；返回 *Handler。
func New(intakeSvc *service.Service, integSvc *integSvc.Service, reqSvc *reqSvc.Service) *Handler {
	return &Handler{
		intakeSvc: intakeSvc,
		integSvc:  integSvc,
		reqSvc:    reqSvc,
	}
}

// CreateConversation 处理 `POST /open/v1/conversations`，支持 `Idempotency-Key` 幂等返回对话或表单模式。
// 参数 c 为 Gin 请求上下文；输出会话模式与可选表单定义 JSON。
func (h *Handler) CreateConversation(c *gin.Context) {
	integ := currentIntegration(c)
	if integ == nil {
		response.Unauthorized(c, "接入方未鉴权")
		return
	}
	idemKey := c.GetHeader("Idempotency-Key")
	if cached, _ := h.integSvc.CheckIdempotency(integ.ID, idemKey); cached != nil {
		c.Data(cached.StatusCode, "application/json; charset=utf-8", []byte(cached.ResponseJSON))
		return
	}

	var in service.CreateConversationInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.BadRequest(c, "新建会话参数格式非法")
		return
	}
	res, err := h.intakeSvc.CreateConversation(c.Request.Context(), integ, in)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	env := response.Envelope{RequestID: response.RequestID(c), Code: 0, Message: "success", Data: res}
	raw, _ := json.Marshal(env)
	h.integSvc.StoreIdempotency(integ.ID, idemKey, c.Request.Method, c.Request.URL.Path, http.StatusOK, string(raw))
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}

// SendMessage 处理 `POST /open/v1/conversations/:id/messages`，以标准 `text/event-stream` SSE 流式输出 `message.delta`、`ask`、`prototype`、`summary`、`status`、`error`、`done` 事件。
// 参数 c 为 Gin 请求上下文；直接写入 SSE 事件流。
func (h *Handler) SendMessage(c *gin.Context) {
	integ := currentIntegration(c)
	if integ == nil {
		response.Unauthorized(c, "接入方未鉴权")
		return
	}
	convID, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var in service.SendMessageInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.BadRequest(c, "消息参数格式非法")
		return
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.WriteHeader(http.StatusOK)

	emit := func(ev service.SSEEvent) {
		raw, _ := json.Marshal(ev.Data)
		_, _ = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", ev.Event, string(raw))
		c.Writer.Flush()
	}

	if err := h.intakeSvc.ProcessUserMessage(c.Request.Context(), integ, convID, in, emit); err != nil {
		emit(service.SSEEvent{Event: "error", Data: map[string]string{"message": err.Error()}})
	}
}

// UploadAttachment 处理 `POST /open/v1/attachments` 上传截图（支持 multipart/form-data，限 png/jpg/webp 且 <= 10 MB）。
// 参数 c 为 Gin 请求上下文；返回附件元数据 JSON。
func (h *Handler) UploadAttachment(c *gin.Context) {
	integ := currentIntegration(c)
	if integ == nil {
		response.Unauthorized(c, "接入方未鉴权")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 11*1024*1024)
	fileHdr, err := c.FormFile("file")
	if err != nil {
		response.BadRequest(c, "请通过 multipart 字段 file 上传不大于 10 MB 的图片")
		return
	}
	f, err := fileHdr.Open()
	if err != nil {
		response.BadRequest(c, "读取上传文件失败")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 10*1024*1024+1))
	if err != nil {
		response.BadRequest(c, "读取上传内容失败")
		return
	}

	productID, _ := strconv.ParseUint(c.PostForm("productId"), 10, 64)
	convID, _ := strconv.ParseUint(c.PostForm("conversationId"), 10, 64)
	att, err := h.intakeSvc.SaveAttachment(integ.ID, productID, convID, fileHdr.Filename, data)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, att)
}

// ConfirmSummary 处理 `POST /open/v1/conversations/:id/confirm`，校验完成门后正式创建需求（支持 `Idempotency-Key`）。
// 参数 c 为 Gin 请求上下文；输出 issueId 与 status。
func (h *Handler) ConfirmSummary(c *gin.Context) {
	integ := currentIntegration(c)
	if integ == nil {
		response.Unauthorized(c, "接入方未鉴权")
		return
	}
	idemKey := c.GetHeader("Idempotency-Key")
	if cached, _ := h.integSvc.CheckIdempotency(integ.ID, idemKey); cached != nil {
		c.Data(cached.StatusCode, "application/json; charset=utf-8", []byte(cached.ResponseJSON))
		return
	}
	convID, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	created, err := h.intakeSvc.ConfirmCard(integ, convID)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	env := response.Envelope{
		RequestID: response.RequestID(c),
		Code:      0,
		Message:   "success",
		Data: gin.H{
			"issueId": created.ID,
			"status":  created.Status,
			"title":   created.Title,
		},
	}
	raw, _ := json.Marshal(env)
	h.integSvc.StoreIdempotency(integ.ID, idemKey, c.Request.Method, c.Request.URL.Path, http.StatusOK, string(raw))
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}

// ListReporterIssues 处理 `GET /open/v1/reporters/:rid/issues`，返回某外部提交人的反馈列表与业务状态。
// 参数 c 为 Gin 请求上下文；输出反馈数组 JSON。
func (h *Handler) ListReporterIssues(c *gin.Context) {
	integ := currentIntegration(c)
	if integ == nil {
		response.Unauthorized(c, "接入方未鉴权")
		return
	}
	rid := c.Param("rid")
	issues, err := h.reqSvc.ListReporterIssues(integ.ID, rid)
	if err != nil {
		response.ServerError(c, "查询提交人反馈列表失败")
		return
	}
	response.OK(c, issues)
}

// GetIssueDetail 处理 `GET /open/v1/issues/:id`，返回需求详情与状态时间线（校验该产品已授权给当前接入方）。
// 参数 c 为 Gin 请求上下文；输出需求详情 JSON。
func (h *Handler) GetIssueDetail(c *gin.Context) {
	integ := currentIntegration(c)
	if integ == nil {
		response.Unauthorized(c, "接入方未鉴权")
		return
	}
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	detail, err := h.reqSvc.GetDetail(id)
	if err != nil || detail == nil {
		response.NotFound(c, "反馈记录不存在")
		return
	}
	if !h.integSvc.IsProductBound(integ, detail.Requirement.ProductID) {
		response.Forbidden(c, "当前接入方无权访问该产品反馈")
		return
	}
	response.OK(c, gin.H{
		"issue":  detail.Requirement,
		"events": detail.Events,
	})
}

// PullEvents 处理 `GET /open/v1/events?cursor=`，供接入方后端增量拉取状态变化、新追问与上线通知。
// 参数 c 为 Gin 请求上下文；输出 events 数组及 nextCursor。
func (h *Handler) PullEvents(c *gin.Context) {
	integ := currentIntegration(c)
	if integ == nil {
		response.Unauthorized(c, "接入方未鉴权")
		return
	}
	cursor, _ := strconv.ParseUint(c.DefaultQuery("cursor", "0"), 10, 64)
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	events, err := h.integSvc.PullEvents(integ.ID, cursor, limit)
	if err != nil {
		response.ServerError(c, "拉取增量事件失败")
		return
	}
	nextCursor := cursor
	for _, ev := range events {
		if ev.ID > nextCursor {
			nextCursor = ev.ID
		}
	}
	response.OK(c, gin.H{
		"events":     events,
		"nextCursor": nextCursor,
	})
}

// currentIntegration 从 Gin 上下文读取已通过 HMAC 验签中间件校验的接入方实体；参数 c 为请求上下文；返回 *integModel.Integration 或 nil。
func currentIntegration(c *gin.Context) *integModel.Integration {
	v, ok := c.Get("autoship.integration")
	if !ok {
		return nil
	}
	integ, _ := v.(*integModel.Integration)
	return integ
}
