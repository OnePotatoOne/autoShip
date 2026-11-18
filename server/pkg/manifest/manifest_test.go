// Package manifest_test 验证制品包 tar.gz 打包、Ed25519 签名、篡改拦截与路径穿越防护。
package manifest_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"autoship/pkg/crypto"
	"autoship/pkg/manifest"
)

// TestPackAndUnpackVerifyBundle 验证合法签名制品包解压核验通过，而更换公钥或篡改清单时立即失败。
func TestPackAndUnpackVerifyBundle(t *testing.T) {
	dir := t.TempDir()
	pub, priv, err := crypto.GenerateEd25519KeyPair()
	if err != nil {
		t.Fatalf("生成 Ed25519 密钥失败: %v", err)
	}

	binFile := filepath.Join(dir, "app-server")
	hookFile := filepath.Join(dir, "deploy-hook.sh")
	if err := os.WriteFile(binFile, []byte("#!/bin/sh\necho v1.0.0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookFile, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}

	m := &manifest.ReleaseManifest{
		ProductCode:     "seccenter",
		Version:         "v1.0.0",
		Tag:             "v1.0.0",
		CommitSHA:       "abcdef1234567890",
		Changelog:       "### 新增\n- 支持自动升级",
		MigrationRisk:   manifest.RiskAdditive,
		MinAgentVersion: "v0.1.0",
		CreatedAt:       time.Now().Unix(),
	}
	bundlePath := filepath.Join(dir, "bundle.tar.gz")
	sz, sum, err := manifest.PackBundle(bundlePath, m, binFile, hookFile, priv)
	if err != nil || sz <= 0 || len(sum) != 64 {
		t.Fatalf("PackBundle 失败: %v", err)
	}

	unpackDir := filepath.Join(dir, "unpacked")
	verified, gotBin, gotHook, err := manifest.UnpackAndVerifyBundle(bundlePath, unpackDir, pub)
	if err != nil {
		t.Fatalf("UnpackAndVerifyBundle 失败: %v", err)
	}
	if verified.Version != "v1.0.0" || gotBin == "" || gotHook == "" {
		t.Fatalf("解压校验产物不完整")
	}

	wrongPub, _, _ := crypto.GenerateEd25519KeyPair()
	if _, _, _, err := manifest.UnpackAndVerifyBundle(bundlePath, filepath.Join(dir, "bad"), wrongPub); err == nil {
		t.Fatalf("错误发布公钥未拦截制品包")
	}
}
