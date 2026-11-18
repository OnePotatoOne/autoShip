// Package router 负责装配 autoShip Server 的全部 14 个业务模块，并构造严格物理隔离的双路由引擎：Tailnet 管理/开发引擎与 Public 开放/更新引擎。
package router

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"autoship/internal/config"
	"autoship/internal/database"
	"autoship/internal/middleware"
	"autoship/internal/modules/agent"
	agentHandler "autoship/internal/modules/agent/handler"
	agentRepo "autoship/internal/modules/agent/repository"
	agentSvc "autoship/internal/modules/agent/service"
	artSvc "autoship/internal/modules/artifact/service"
	"autoship/internal/modules/audit"
	auditHandler "autoship/internal/modules/audit/handler"
	auditRepo "autoship/internal/modules/audit/repository"
	auditSvc "autoship/internal/modules/audit/service"
	"autoship/internal/modules/auth"
	authHandler "autoship/internal/modules/auth/handler"
	authRepo "autoship/internal/modules/auth/repository"
	authSvc "autoship/internal/modules/auth/service"
	"autoship/internal/modules/deploy"
	deployHandler "autoship/internal/modules/deploy/handler"
	deployRepo "autoship/internal/modules/deploy/repository"
	deploySvc "autoship/internal/modules/deploy/service"
	"autoship/internal/modules/intake"
	intakeHandler "autoship/internal/modules/intake/handler"
	intakeRepo "autoship/internal/modules/intake/repository"
	intakeSvc "autoship/internal/modules/intake/service"
	"autoship/internal/modules/integration"
	integHandler "autoship/internal/modules/integration/handler"
	integRepo "autoship/internal/modules/integration/repository"
	integSvc "autoship/internal/modules/integration/service"
	llmRepo "autoship/internal/modules/llm/repository"
	llmSvc "autoship/internal/modules/llm/service"
	notifySvc "autoship/internal/modules/notify/service"
	"autoship/internal/modules/product"
	prodHandler "autoship/internal/modules/product/handler"
	prodRepo "autoship/internal/modules/product/repository"
	prodSvc "autoship/internal/modules/product/service"
	"autoship/internal/modules/release"
	relHandler "autoship/internal/modules/release/handler"
	relRepo "autoship/internal/modules/release/repository"
	relSvc "autoship/internal/modules/release/service"
	"autoship/internal/modules/requirement"
	reqHandler "autoship/internal/modules/requirement/handler"
	reqRepo "autoship/internal/modules/requirement/repository"
	reqSvc "autoship/internal/modules/requirement/service"
	runRepo "autoship/internal/modules/run/repository"
	runSvc "autoship/internal/modules/run/service"
	"autoship/internal/modules/work"
	workHandler "autoship/internal/modules/work/handler"
	workRepo "autoship/internal/modules/work/repository"
	workSvc "autoship/internal/modules/work/service"
	"autoship/internal/static"
	"autoship/pkg/pki"
	"autoship/pkg/response"
)

// AppBundle 保存装配完毕的双 HTTP 路由引擎与后台任务服务句柄。
type AppBundle struct {
	TailnetEngine *gin.Engine     // 只监听 tailnet 地址的管理后台与开发 Agent 引擎
	PublicEngine  *gin.Engine     // 监听公网端口并带严格 IP 白名单的 Open API 与更新 Agent 引擎
	LLMService    *llmSvc.Service // 用于启动后台探活循环的模型服务
}

// Build 实例化全部仓储、业务服务与控制器，完成跨模块桥接，并返回双监听器 Gin 引擎。
// 参数 cfg/cfgPath/encKey 为全局配置与密钥，ca 为根 CA Bundle，dbProvider 为获取当前 *gorm.DB 的函数；返回 *AppBundle。
func Build(cfg *config.ServerConfig, cfgPath string, encKey []byte, ca *pki.Bundle, dbProvider func() *gorm.DB) *AppBundle {
	gin.SetMode(gin.ReleaseMode)
	if dbProvider == nil {
		dbProvider = database.Get
	}

	// 1. 初始化各模块仓储与无环核心服务
	notifier := notifySvc.New(cfg)

	audR := auditRepo.New(dbProvider)
	audS := auditSvc.New(audR, cfg, cfgPath, encKey)
	audH := auditHandler.New(audS, ca)

	auR := authRepo.New(dbProvider)
	auS := authSvc.New(auR, encKey)
	auH := authHandler.New(auS, cfg, cfgPath, encKey, ca)

	prR := prodRepo.New(dbProvider)
	prS := prodSvc.New(prR)
	prH := prodHandler.New(prS)

	inR := integRepo.New(dbProvider)
	inS := integSvc.New(inR, encKey)
	inH := integHandler.New(inS)

	agR := agentRepo.New(dbProvider)
	agS := agentSvc.New(agR, cfg, ca, notifier)
	agH := agentHandler.New(agS, cfg, ca)

	llR := llmRepo.New(dbProvider)
	llS := llmSvc.New(llR, cfg, encKey, ca, agS)

	rqR := reqRepo.New(dbProvider)
	rqS := reqSvc.New(rqR, prS, inS, llS, notifier)
	rqH := reqHandler.New(rqS)

	ikR := intakeRepo.New(dbProvider)
	ikS := intakeSvc.New(ikR, cfg, prS, inS, rqS, llS)
	ikH := intakeHandler.New(ikS, inS, rqS)

	rnR := runRepo.New(dbProvider)
	rnS := runSvc.New(rnR)

	wkR := workRepo.New(dbProvider)
	wkS := workSvc.New(wkR, prS, agS, rnS, llS, notifier)
	wkH := workHandler.New(wkS)

	arS := artSvc.New(cfg)
	rlR := relRepo.New(dbProvider)
	rlS := relSvc.New(rlR, arS, prS, agS, notifier)
	rlH := relHandler.New(rlS, arS)

	dpR := deployRepo.New(dbProvider)
	dpS := deploySvc.New(dpR, cfg, encKey, prS, agS, notifier)
	dpH := deployHandler.New(dpS)

	// 2. 注入跨模块协作接口，消除包级循环依赖
	agS.SetTargetLookup(dpS)
	agH.SetDevEventConsumer(wkS)
	rqS.SetCollaborators(wkS, ikS)
	wkS.SetBridges(rqS, rlS)
	rlS.SetBridges(wkS, dpS, rqS)
	dpS.SetReleaseNotifier(rlS)
	llS.SetOnRecovered(func() {
		ikS.DrainPendingFollowUps()
		rqS.DrainPendingTriage()
	})

	// 3. 构造 Tailnet 引擎（管理后台 UI/API + 开发 Agent 接口）
	tailnet := gin.New()
	tailnet.Use(gin.Recovery(), middleware.RequestID(), middleware.SecurityHeaders())

	adminPublic := tailnet.Group("/api/v1/admin")
	adminPublic.Use(middleware.AuditLogger(audS, "admin"))
	auth.RegisterPublicRoutes(adminPublic, auH)

	adminProtected := tailnet.Group("/api/v1/admin")
	adminProtected.Use(middleware.AdminAuth(cfg, auS), middleware.AuditLogger(audS, "admin"))
	auth.RegisterProtectedRoutes(adminProtected, auH)
	adminProtected.GET("/overview", buildOverviewHandler(prS, rqR, wkR, rlR, dpR, agS, llS))
	product.RegisterAdminRoutes(adminProtected, prH)
	integration.RegisterAdminRoutes(adminProtected, inH)
	requirement.RegisterAdminRoutes(adminProtected, rqH)
	work.RegisterAdminRoutes(adminProtected, wkH)
	release.RegisterAdminRoutes(adminProtected, rlH)
	deploy.RegisterAdminRoutes(adminProtected, dpH)
	agent.RegisterAdminRoutes(adminProtected, agH)
	audit.RegisterAdminRoutes(adminProtected, audH)

	devPublic := tailnet.Group("/agent/v1/dev")
	devPublic.Use(middleware.AuditLogger(audS, "dev_agent"))
	devAuthed := tailnet.Group("/agent/v1/dev")
	devAuthed.Use(middleware.AgentAuth(agS, "dev"), middleware.AuditLogger(audS, "dev_agent"))
	agent.RegisterDevRoutes(devPublic, devAuthed, agH)
	release.RegisterDevRoutes(devAuthed, rlH)

	mountEmbeddedSPA(tailnet)

	// 4. 构造 Public 公网机器对机器引擎（绝不挂载管理后台或开发接口！）
	public := gin.New()
	public.Use(
		gin.Recovery(),
		middleware.RequestID(),
		middleware.SecurityHeaders(),
		middleware.PublicIPAllowlist(cfg, inS, agS, dpS),
	)

	openV1 := public.Group("/open/v1")
	openV1.Use(middleware.OpenAPIAuth(inS), middleware.AuditLogger(audS, "open_api"))
	intake.RegisterOpenV1Routes(openV1, ikH)

	updPublic := public.Group("/agent/v1/deploy")
	updPublic.Use(middleware.AuditLogger(audS, "update_agent"))
	updAuthed := public.Group("/agent/v1/deploy")
	updAuthed.Use(middleware.AgentAuth(agS, "update"), middleware.AuditLogger(audS, "update_agent"))
	agent.RegisterUpdateRoutes(updPublic, updAuthed, agH)
	release.RegisterUpdateRoutes(updAuthed, rlH)
	deploy.RegisterUpdateRoutes(updAuthed, dpH)

	return &AppBundle{
		TailnetEngine: tailnet,
		PublicEngine:  public,
		LLMService:    llS,
	}
}

// buildOverviewHandler 组装 5.2 & 5.4 节要求的总览页数据（各状态计数、按产品计数、每个开发任务 token 用量与耗时、agent 在线状态、模型可用性、最近发布与部署）。
func buildOverviewHandler(
	products *prodSvc.Service,
	reqR *reqRepo.Repository,
	workR *workRepo.Repository,
	relR *relRepo.Repository,
	depR *deployRepo.Repository,
	agents *agentSvc.Service,
	llm *llmSvc.Service,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		prods, _ := products.List()
		pendingReq, _ := reqR.CountByStatus("submitted", "triaged", "scheduled", "pending_ai")
		inProgressWork, _ := workR.CountByStatus("preparing", "awaiting_plan", "coding", "awaiting_answer")
		pendingAcc, _ := workR.CountByStatus("pending_acceptance")
		pendingDep, _ := relR.CountByStatus("pending_deploy")

		byProduct := make([]gin.H, 0, len(prods))
		for _, p := range prods {
			_, total, _ := reqR.List(reqRepo.Filter{ProductID: p.ID, Page: 1, PageSize: 1})
			_, inProg, _ := reqR.List(reqRepo.Filter{ProductID: p.ID, Status: "in_progress", Page: 1, PageSize: 1})
			_, relCnt, _ := reqR.List(reqRepo.Filter{ProductID: p.ID, Status: "released", Page: 1, PageSize: 1})
			byProduct = append(byProduct, gin.H{
				"productId":   p.ID,
				"productName": p.Name,
				"total":       total,
				"inProgress":  inProg,
				"deployed":    relCnt,
			})
		}

		mode, healthy, relayOnline, checkedAt := llm.HealthSnapshot()
		agentList, _ := agents.ListWithOnlineStatus()
		recentReleases, _, _ := relR.List(0, "", 1, 8)
		recentDeploys, _, _ := depR.ListDeployments(0, 0, 0, "", 1, 8)
		recentWorks, _, _ := workR.List(0, "", 1, 10)

		workUsage := make([]gin.H, 0, len(recentWorks))
		for _, w := range recentWorks {
			workUsage = append(workUsage, gin.H{
				"workItemId":       w.ID,
				"title":            w.Title,
				"promptTokens":     w.PromptTokensUsed,
				"completionTokens": w.CompletionTokensUsed,
				"totalTokens":      w.PromptTokensUsed + w.CompletionTokensUsed,
				"durationSeconds":  w.DurationSeconds,
				"status":           w.Status,
			})
		}

		response.OK(c, gin.H{
			"counts": gin.H{
				"pendingRequirements": pendingReq,
				"inProgressWork":      inProgressWork,
				"pendingAcceptance":   pendingAcc,
				"pendingDeploy":       pendingDep,
				"totalProducts":       len(prods),
				"byProduct":           byProduct,
			},
			"llmStatus": gin.H{
				"mode":             mode,
				"healthy":          healthy,
				"relayAgentOnline": relayOnline,
				"lastCheckedAt":    checkedAt,
			},
			"agents":            agentList,
			"recentReleases":    recentReleases,
			"recentDeployments": recentDeploys,
			"recentWorkUsage":   workUsage,
		})
	}
}

// mountEmbeddedSPA 将编译期内嵌的 Angular 管理后台静态资源挂载到 Tailnet 引擎根路径，并对前端路由执行 index.html 回退。
func mountEmbeddedSPA(engine *gin.Engine) {
	subFS, err := fs.Sub(static.FS, "dist")
	if err != nil {
		return
	}
	fileServer := http.FileServer(http.FS(subFS))

	engine.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/agent/") {
			response.NotFound(c, "接口路径不存在")
			return
		}
		trimmed := strings.TrimPrefix(p, "/")
		if trimmed != "" {
			if f, openErr := subFS.Open(trimmed); openErr == nil {
				_ = f.Close()
				fileServer.ServeHTTP(c.Writer, c.Request)
				return
			}
		}
		indexBytes, readErr := fs.ReadFile(subFS, "index.html")
		if readErr != nil {
			indexBytes, _ = fs.ReadFile(subFS, ".gitkeep")
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexBytes)
	})
}
