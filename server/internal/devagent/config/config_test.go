package config

import (
	"path/filepath"
	"testing"
)

// TestLoadExampleAndDefaults 验证示例 YAML 配置文件能被正常加载、补齐默认值并通过合法性校验。
func TestLoadExampleAndDefaults(t *testing.T) {
	examplePath := filepath.Clean("../../../../configs/dev-agent.example.yaml")
	cfg, err := Load(examplePath)
	if err != nil {
		t.Fatalf("加载示例配置失败: %v", err)
	}
	if cfg.AgentID != "mac-dev-agent-01" {
		t.Fatalf("期望 agentId=mac-dev-agent-01，实际 %s", cfg.AgentID)
	}
	if len(cfg.Models.Profiles) != 1 || cfg.Models.Profiles[0].Model != "deepseek-v4.1-flash" {
		t.Fatalf("默认模型配置不符合预期: %+v", cfg.Models.Profiles)
	}
	coderProfiles := cfg.ProfilesForRole("coder")
	if len(coderProfiles) != 1 || coderProfiles[0].BaseURL != "http://127.0.0.1:8788/v1" {
		t.Fatalf("coder 角色模型映射异常: %+v", coderProfiles)
	}
	if len(cfg.Products) != 1 || cfg.Products[0].Sandbox.Mode != "protected" {
		t.Fatalf("产品默认权限档案异常: %+v", cfg.Products)
	}
}

// TestValidateRejectsPlainHTTP 验证非 HTTPS 的 ServerURL 会被配置校验器拒绝。
func TestValidateRejectsPlainHTTP(t *testing.T) {
	cfg := Default()
	cfg.ServerURL = "http://100.64.0.10:8444"
	if err := cfg.Validate(); err == nil {
		t.Fatal("期望明文 http:// ServerURL 校验失败，但返回了 nil")
	}

	tmpFile := filepath.Join(t.TempDir(), "agent.yaml")
	cfg.ServerURL = "https://100.64.0.10:8444"
	if err := Save(tmpFile, cfg); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	loaded, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("重新加载配置失败: %v", err)
	}
	if loaded.ServerURL != cfg.ServerURL {
		t.Fatalf("加载后的 ServerURL 不一致: %s", loaded.ServerURL)
	}
}
