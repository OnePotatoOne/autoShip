package publisher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"autoship/internal/devagent/config"
	"autoship/pkg/crypto"
	"autoship/pkg/keychain"
	"autoship/pkg/manifest"
)

// TestPublishPipelineEndToEndResumableUploadAndConflictGuard 验证完整 9 步自动发布流水线：含 rebase 冲突打回重验、CHANGELOG 单文件提交强检、CI Range 续传下载、Ed25519 签名打包与分片续传上传。
func TestPublishPipelineEndToEndResumableUploadAndConflictGuard(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	// 1. 初始化模拟远端 bare 仓库与任务克隆目录
	bareOrigin := filepath.Join(tmp, "origin.git")
	mustRunGit(t, tmp, "init", "--bare", "-b", "main", bareOrigin)

	seedClone := filepath.Join(tmp, "seed")
	mustRunGit(t, tmp, "clone", bareOrigin, seedClone)
	mustRunGit(t, seedClone, "config", "user.name", "Owner")
	mustRunGit(t, seedClone, "config", "user.email", "owner@example.com")
	mustWrite(t, filepath.Join(seedClone, "app.go"), "package main\nfunc main() {}\n")
	mustWrite(t, filepath.Join(seedClone, "deploy/ctl.sh"), "#!/bin/sh\necho deploy-ok\n")
	mustRunGit(t, seedClone, "add", ".")
	mustRunGit(t, seedClone, "commit", "-m", "init main")
	mustRunGit(t, seedClone, "push", "origin", "main")

	// 拉出任务工作树并提交候选功能改动
	wt := filepath.Join(tmp, "worktree")
	mustRunGit(t, tmp, "clone", bareOrigin, wt)
	mustRunGit(t, wt, "config", "user.name", "DevAgent")
	mustRunGit(t, wt, "config", "user.email", "agent@autoship.local")
	mustRunGit(t, wt, "checkout", "-b", "autoship/w-10-feat")
	mustWrite(t, filepath.Join(wt, "feature.txt"), "new feature payload\n")
	mustRunGit(t, wt, "add", ".")
	mustRunGit(t, wt, "commit", "-m", "feat: add feature")
	acceptedSHA := mustRunGit(t, wt, "rev-parse", "HEAD")

	// 准备钥匙串密钥（发布签名密钥 + Agent 身份密钥）
	releasePub, releasePriv, err := crypto.GenerateEd25519KeyPair()
	if err != nil {
		t.Fatal(err)
	}
	_, agentPriv, err := crypto.GenerateEd25519KeyPair()
	if err != nil {
		t.Fatal(err)
	}
	secrets := keychain.NewMemoryStore()
	_ = secrets.Set(ReleaseKeychainService, ReleaseKeychainAccount, releasePriv)
	_ = secrets.Set("autoship.devagent", AgentIdentityAccount, agentPriv)

	// 2. 模拟支持 Range 断点续传的 GitHub REST API 服务 + 支持分片续传合并的 autoShip Server
	artifactPayload := bytes.Repeat([]byte("LINUX_AMD64_BINARY_SEGMENT_"), 400) // ~10.8 KB
	dropFirstDownload := true
	var ghSrv *httptest.Server
	ghSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/actions/runs"):
			headSHA := r.URL.Query().Get("head_sha")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"workflow_runs": []map[string]any{
					{
						"id":         7788,
						"name":       "release.yml",
						"head_sha":   headSHA,
						"status":     "completed",
						"conclusion": "success",
						"html_url":   ghSrv.URL + "/runs/7788",
					},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/actions/runs/7788/artifacts"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"artifacts": []map[string]any{
					{
						"name":                 "demo-app-linux-amd64",
						"archive_download_url": ghSrv.URL + "/download/demo-bin",
					},
				},
			})
		case r.URL.Path == "/download/demo-bin":
			rangeHdr := r.Header.Get("Range")
			if dropFirstDownload && rangeHdr == "" {
				// 首次下载写一半故意截断关闭，测试客户端自动发 Range: bytes=<offset>- 续传！
				dropFirstDownload = false
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(artifactPayload[:2000])
				if hj, ok := w.(http.Hijacker); ok {
					conn, _, _ := hj.Hijack()
					if conn != nil {
						_ = conn.Close()
					}
				}
				return
			}
			if strings.HasPrefix(rangeHdr, "bytes=") {
				offStr := strings.TrimSuffix(strings.TrimPrefix(rangeHdr, "bytes="), "-")
				off, _ := strconv.Atoi(offStr)
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(artifactPayload[off:])
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(artifactPayload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ghSrv.Close()

	var srvMu sync.Mutex
	receivedChunks := make(map[int][]byte)
	completedCalled := false

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srvMu.Lock()
		defer srvMu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/upload/init"):
			// 预置模拟分片 0 此前已上传成功，验证客户端跳过分片 0 仅上传后续分片
			_ = json.NewEncoder(w).Encode(UploadInitResponse{
				UploadID:       "up-session-1",
				UploadedChunks: []int{0},
			})
		case strings.Contains(r.URL.Path, "/upload/chunk/"):
			idxStr := filepath.Base(r.URL.Path)
			idx, _ := strconv.Atoi(idxStr)
			data, _ := io.ReadAll(r.Body)
			receivedChunks[idx] = data
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/upload/complete"):
			completedCalled = true
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	agentCfg := config.Default()
	agentCfg.RootDir = filepath.Join(tmp, "agent-root")
	agentCfg.ServerURL = mockServer.URL

	pipe, err := NewPipeline(agentCfg, secrets, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pipe.SetGitHubBaseURL(ghSrv.URL)
	// 将分片大小设为 128 字节以触发多分片（gzip 压缩后仍有多片）与跳过已上传分片 0 的路径
	pipe.SetServerHTTPClient(mockServer.Client(), 128)

	prod := config.ProductProfile{
		ProductCode: "demo-app",
		DefaultBranch: "main",
		GitHubOwner: "example",
		GitHubRepo:  "demo-app",
		Release: config.ReleaseRecipeConfig{
			ChangelogFile:     "CHANGELOG.md",
			CIWorkflowName:    "release.yml",
			ArtifactAssetName: "demo-app-linux-amd64",
			HookRepoPath:      "deploy/ctl.sh",
			MinAgentVersion:   "v0.1.0",
		},
	}

	res, err := pipe.Publish(ctx, &PublishRequest{
		ReleaseID:         88,
		WorkID:            "w-10",
		Product:           prod,
		WorktreeDir:       wt,
		TaskBranch:        "autoship/w-10-feat",
		AcceptedCommitSHA: acceptedSHA,
		LatestVersion:     "v1.2.3",
		HasFeature:        true,
		ChangelogBody:     "- 新增批量导出能力",
		MigrationRisk:     manifest.RiskAdditive,
	})
	if err != nil {
		t.Fatalf("Publish 全流程失败: %v", err)
	}
	if res.Version != "v1.3.0" || res.Tag != "v1.3.0" {
		t.Fatalf("含新功能版本号应当从 v1.2.3 升为 v1.3.0，实际 %s", res.Version)
	}

	// 验证打包后的 .tar.gz 能用发布公钥完整解压并通过签名与哈希校验
	unpackedM, unpackedBin, _, err := manifest.UnpackAndVerifyBundle(res.BundlePath, filepath.Join(tmp, "verify"), releasePub)
	if err != nil {
		t.Fatalf("打包制品验签解包失败: %v", err)
	}
	binBytes, _ := os.ReadFile(unpackedBin)
	if !bytes.Equal(binBytes, artifactPayload) || unpackedM.MigrationRisk != manifest.RiskAdditive {
		t.Fatalf("续传下载并打包后的制品字节不一致 (len=%d vs %d)", len(binBytes), len(artifactPayload))
	}

	srvMu.Lock()
	_, chunk0Sent := receivedChunks[0]
	chunkCount := len(receivedChunks)
	done := completedCalled
	srvMu.Unlock()
	if chunk0Sent || chunkCount == 0 || !done {
		t.Fatalf("分片断点续传行为异常: chunk0Sent=%v chunkCount=%d completed=%v", chunk0Sent, chunkCount, done)
	}

	// 3. 验证 CHANGELOG 单文件提交强检：若夹带修改了代码文件必须报错
	mustWrite(t, filepath.Join(wt, "CHANGELOG.md"), "update\n")
	mustWrite(t, filepath.Join(wt, "app.go"), "package main\nfunc tampered() {}\n")
	mustRunGit(t, wt, "add", ".")
	mustRunGit(t, wt, "commit", "-m", "bad changelog commit")
	if err := VerifyChangelogOnlyCommit(ctx, wt, "HEAD", "CHANGELOG.md"); !errors.Is(err, ErrChangelogCommitTouchedCode) {
		t.Fatalf("夹带代码改动的变更日志提交必须返回 ErrChangelogCommitTouchedCode，实际: %v", err)
	}

	// 4. 验证主分支推进产生冲突时，Publish 立即中止 rebase 并返回 ErrRebaseConflictNeedReaccept
	wtConflict := filepath.Join(tmp, "wt-conflict")
	mustRunGit(t, tmp, "clone", bareOrigin, wtConflict)
	mustRunGit(t, wtConflict, "config", "user.name", "DevAgent")
	mustRunGit(t, wtConflict, "config", "user.email", "agent@autoship.local")
	mustRunGit(t, wtConflict, "checkout", "-b", "autoship/w-11-conflict")
	mustWrite(t, filepath.Join(wtConflict, "app.go"), "package main\nfunc taskSide() {}\n")
	mustRunGit(t, wtConflict, "add", ".")
	mustRunGit(t, wtConflict, "commit", "-m", "task side change")
	conflictAcceptedSHA := mustRunGit(t, wtConflict, "rev-parse", "HEAD")

	// 模拟另一版本先行推进了远端 main 的同一行代码制造冲突
	mustRunGit(t, seedClone, "pull", "--rebase", "origin", "main")
	mustWrite(t, filepath.Join(seedClone, "app.go"), "package main\nfunc mainSideConflict() {}\n")
	mustRunGit(t, seedClone, "add", ".")
	mustRunGit(t, seedClone, "commit", "-m", "main side conflicting change")
	mustRunGit(t, seedClone, "push", "origin", "main")

	_, conflictErr := pipe.Publish(ctx, &PublishRequest{
		ReleaseID:         89,
		WorkID:            "w-11",
		Product:           prod,
		WorktreeDir:       wtConflict,
		TaskBranch:        "autoship/w-11-conflict",
		AcceptedCommitSHA: conflictAcceptedSHA,
		LatestVersion:     "v1.3.0",
	})
	if !errors.Is(conflictErr, ErrRebaseConflictNeedReaccept) {
		t.Fatalf("主分支冲突必须返回 ErrRebaseConflictNeedReaccept 退回重过验收，实际: %v", conflictErr)
	}
}

// mustRunGit 在测试目录运行 git 命令并返回输出文本。
func mustRunGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v 失败: %v (%s)", args, err, string(out))
	}
	return strings.TrimSpace(string(out))
}

// mustWrite 写入测试文件并自动创建父目录。
func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
