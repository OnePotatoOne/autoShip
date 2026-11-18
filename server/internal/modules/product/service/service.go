// Package service 实现产品档案聚合查询、权限档案拦截记录与后台一键放行服务。
package service

import (
	"encoding/json"
	"errors"
	"strings"

	"autoship/internal/modules/product/model"
	"autoship/internal/modules/product/repository"
	"autoship/pkg/envelope"
)

// ProductFullBundle 聚合单个产品及其全部模块、范围规则、权限档案与知识条目（供管理后台详情页与下发开发 Agent 任务规格共用）。
type ProductFullBundle struct {
	Product           *model.Product                  `json:"product"`           // 产品主档案
	Modules           []model.ProductModule           `json:"modules"`           // 模块列表
	ScopeRules        []model.ProductScopeRule        `json:"scopeRules"`        // 范围与公共区规则
	PermissionProfile *model.ProductPermissionProfile `json:"permissionProfile"` // 沙箱权限档案
	Knowledge         []model.ProductKnowledge        `json:"knowledge"`         // 产品知识库
}

// Service 提供产品档案全生命周期业务逻辑。
type Service struct {
	repo *repository.Repository
}

// New 创建产品业务服务实例。
// 参数 repo 为产品仓储；返回 *Service。
func New(repo *repository.Repository) *Service {
	return &Service{repo: repo}
}

// List 返回全部产品主档案列表。
// 无入参；返回产品切片与错误。
func (s *Service) List() ([]model.Product, error) {
	return s.repo.ListProducts()
}

// GetByCode 按产品唯一编码读取产品主档案。
// 参数 code 为产品编码；返回 *model.Product 与错误。
func (s *Service) GetByCode(code string) (*model.Product, error) {
	return s.repo.GetProductByCode(code)
}

// GetByID 按产品主键读取产品主档案。
// 参数 id 为产品主键；返回 *model.Product 与错误。
func (s *Service) GetByID(id uint64) (*model.Product, error) {
	return s.repo.GetProductByID(id)
}

// GetFullBundle 加载指定产品的完整聚合档案。
// 参数 id 为产品主键；不存在时返回 (nil, nil)，成功返回 *ProductFullBundle。
func (s *Service) GetFullBundle(id uint64) (*ProductFullBundle, error) {
	p, err := s.repo.GetProductByID(id)
	if err != nil || p == nil {
		return nil, err
	}
	mods, err := s.repo.ListModules(id)
	if err != nil {
		return nil, err
	}
	rules, err := s.repo.ListScopeRules(id)
	if err != nil {
		return nil, err
	}
	prof, err := s.repo.GetOrInitPermissionProfile(id)
	if err != nil {
		return nil, err
	}
	kn, err := s.repo.ListKnowledge(id, "")
	if err != nil {
		return nil, err
	}
	return &ProductFullBundle{
		Product:           p,
		Modules:           mods,
		ScopeRules:        rules,
		PermissionProfile: prof,
		Knowledge:         kn,
	}, nil
}

// Create 校验安全编码并新建产品档案。
// 参数 p 为产品模型指针；返回创建错误。
func (s *Service) Create(p *model.Product) error {
	p.Code = strings.ToLower(strings.TrimSpace(p.Code))
	p.Name = strings.TrimSpace(p.Name)
	if err := envelope.ValidateSafeID(p.Code); err != nil {
		return errors.New("产品编码只能包含字母、数字、点、横线或下划线（1-64字符）")
	}
	if p.Name == "" {
		return errors.New("产品名称不能为空")
	}
	if p.DefaultBranch == "" {
		p.DefaultBranch = "main"
	}
	if p.CommandTimeoutSec <= 0 {
		p.CommandTimeoutSec = 900
	}
	if p.PreviewPortCount <= 0 {
		p.PreviewPortCount = 1
	}
	if p.HealthCheckPath == "" {
		p.HealthCheckPath = "/api/healthz"
	}
	if p.ChangelogFile == "" {
		p.ChangelogFile = "CHANGELOG.md"
	}
	return s.repo.CreateProduct(p)
}

// Update 更新已有产品档案配置。
// 参数 id 为产品主键，in 为提交的新配置；返回更新后的 *model.Product 及错误。
func (s *Service) Update(id uint64, in model.Product) (*model.Product, error) {
	existing, err := s.repo.GetProductByID(id)
	if err != nil || existing == nil {
		return nil, errors.New("目标产品不存在")
	}
	if strings.TrimSpace(in.Name) != "" {
		existing.Name = strings.TrimSpace(in.Name)
	}
	existing.Description = in.Description
	existing.RepoURL = strings.TrimSpace(in.RepoURL)
	if in.DefaultBranch != "" {
		existing.DefaultBranch = strings.TrimSpace(in.DefaultBranch)
	}
	existing.InstallCmd = in.InstallCmd
	existing.BuildCmd = in.BuildCmd
	existing.TestCmd = in.TestCmd
	existing.LintCmd = in.LintCmd
	if in.CommandTimeoutSec > 0 {
		existing.CommandTimeoutSec = in.CommandTimeoutSec
	}
	existing.PreviewStartCmd = in.PreviewStartCmd
	if in.PreviewPortCount > 0 {
		existing.PreviewPortCount = in.PreviewPortCount
	}
	if in.PreviewDBPrefix != "" {
		existing.PreviewDBPrefix = in.PreviewDBPrefix
	}
	existing.PreviewSeedCmd = in.PreviewSeedCmd
	existing.PreviewSupportsTLS = in.PreviewSupportsTLS
	if in.HealthCheckPath != "" {
		existing.HealthCheckPath = in.HealthCheckPath
	}
	if in.ChangelogFile != "" {
		existing.ChangelogFile = in.ChangelogFile
	}
	if in.DeployHookPath != "" {
		existing.DeployHookPath = in.DeployHookPath
	}
	existing.ReleaseSignPubB64 = strings.TrimSpace(in.ReleaseSignPubB64)
	existing.DevRulesPrompt = in.DevRulesPrompt
	existing.IntakeEnabled = in.IntakeEnabled
	existing.AutoDevEnabled = in.AutoDevEnabled
	existing.AutoDeployEnabled = in.AutoDeployEnabled
	existing.PlanConfirmDefault = in.PlanConfirmDefault

	if err := s.repo.UpdateProduct(existing); err != nil {
		return nil, err
	}
	return existing, nil
}

// Delete 删除指定产品档案。
// 参数 id 为产品主键；返回删除错误。
func (s *Service) Delete(id uint64) error {
	return s.repo.DeleteProduct(id)
}

// SaveModules 保存产品的模块目录映射列表。
// 参数 productID 为产品 ID，modules 为模块列表；返回错误。
func (s *Service) SaveModules(productID uint64, modules []model.ProductModule) error {
	return s.repo.ReplaceModules(productID, modules)
}

// SaveScopeRules 保存产品的公共区与受保护路径规则。
// 参数 productID 为产品 ID，rules 为范围规则列表；返回错误。
func (s *Service) SaveScopeRules(productID uint64, rules []model.ProductScopeRule) error {
	return s.repo.ReplaceScopeRules(productID, rules)
}

// UpdatePermissionProfile 更新产品的沙箱隔离档位与自定义读写路径清单。
// 参数 productID 为产品 ID，mode 为 off/protected/custom，allowedWrite/extraDenied 为路径字符串数组；返回更新后的权限档案与错误。
func (s *Service) UpdatePermissionProfile(productID uint64, mode string, allowedWrite, extraDenied []string) (*model.ProductPermissionProfile, error) {
	switch mode {
	case "off", "protected", "custom":
	default:
		return nil, errors.New("权限档位必须是 off、protected 或 custom")
	}
	prof, err := s.repo.GetOrInitPermissionProfile(productID)
	if err != nil {
		return nil, err
	}
	if allowedWrite == nil {
		allowedWrite = []string{}
	}
	if extraDenied == nil {
		extraDenied = []string{}
	}
	awBytes, _ := json.Marshal(allowedWrite)
	edBytes, _ := json.Marshal(extraDenied)
	prof.Mode = mode
	prof.AllowedWritePathsJSON = string(awBytes)
	prof.ExtraDeniedReadJSON = string(edBytes)
	if err := s.repo.SavePermissionProfile(prof); err != nil {
		return nil, err
	}
	return prof, nil
}

// AllowBlockedPath 在管理后台一键将沙箱拦截路径加入该产品的允许清单并切换至 custom 档位（8.3 节）。
// 参数 productID 为产品 ID，blockedPath 为被拦截的文件或目录路径；返回更新后的权限档案与错误。
func (s *Service) AllowBlockedPath(productID uint64, blockedPath string) (*model.ProductPermissionProfile, error) {
	cleanPath := strings.TrimSpace(blockedPath)
	if cleanPath == "" {
		return nil, errors.New("放行路径不能为空")
	}
	prof, err := s.repo.GetOrInitPermissionProfile(productID)
	if err != nil {
		return nil, err
	}
	var allowed []string
	if prof.AllowedWritePathsJSON != "" {
		_ = json.Unmarshal([]byte(prof.AllowedWritePathsJSON), &allowed)
	}
	exists := false
	for _, p := range allowed {
		if p == cleanPath {
			exists = true
			break
		}
	}
	if !exists {
		allowed = append(allowed, cleanPath)
	}
	raw, _ := json.Marshal(allowed)
	prof.AllowedWritePathsJSON = string(raw)
	if prof.Mode == "protected" {
		prof.Mode = "custom"
	}
	if prof.LastBlockedPath == cleanPath {
		prof.LastBlockedPath = ""
	}
	if err := s.repo.SavePermissionProfile(prof); err != nil {
		return nil, err
	}
	return prof, nil
}

// RecordBlockedPath 由开发 Agent 上报最近一次被权限档案拦截的路径，便于后台卡片展示一键放行入口。
// 参数 productID 为产品 ID，blockedPath 为拦截路径；返回保存错误。
func (s *Service) RecordBlockedPath(productID uint64, blockedPath string) error {
	prof, err := s.repo.GetOrInitPermissionProfile(productID)
	if err != nil {
		return err
	}
	prof.LastBlockedPath = strings.TrimSpace(blockedPath)
	return s.repo.SavePermissionProfile(prof)
}

// SaveKnowledge 保存人工编辑的产品知识库条目。
// 参数 productID 为产品 ID，items 为知识切片；返回错误。
func (s *Service) SaveKnowledge(productID uint64, items []model.ProductKnowledge) error {
	return s.repo.ReplaceManualKnowledge(productID, items)
}

// SearchKnowledge 检索某产品的知识库（供客服 AI lookup_product 工具调用）。
// 参数 productID 为产品 ID，query 为检索关键词；返回匹配的知识列表与错误。
func (s *Service) SearchKnowledge(productID uint64, query string) ([]model.ProductKnowledge, error) {
	return s.repo.ListKnowledge(productID, query)
}

// SyncProjectMap 更新某产品的自动项目地图知识。
// 参数 productID 为产品 ID，content 为项目地图文本；返回错误。
func (s *Service) SyncProjectMap(productID uint64, content string) error {
	return s.repo.SyncAutoProjectMap(productID, content)
}
