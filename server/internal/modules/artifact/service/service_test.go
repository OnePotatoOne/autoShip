// Package service_test 验证制品存储服务的分片续传合并、哈希校验、HTTP Range 断点下载与保留最近 N 个版本的清理逻辑。
package service_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"autoship/internal/config"
	artSvc "autoship/internal/modules/artifact/service"
)

// TestChunkedUploadRangeDownloadAndPrune 验证多片乱序上传合并、HTTP 206 Range 断点续传及历史版本淘汰。
func TestChunkedUploadRangeDownloadAndPrune(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.ArtifactKeepCount = 2
	svc := artSvc.New(cfg)

	var lastRelPath string
	for relID := uint64(1); relID <= 3; relID++ {
		fullData := bytes.Repeat([]byte(fmt.Sprintf("chunk-payload-%d-", relID)), 128)
		sum := sha256.Sum256(fullData)
		sumHex := hex.EncodeToString(sum[:])

		upID, err := svc.InitUpload(relID, "seccenter", fmt.Sprintf("v0.%d.0", relID), int64(len(fullData)), sumHex, "{}")
		if err != nil {
			t.Fatalf("InitUpload 失败: %v", err)
		}
		mid := len(fullData) / 2
		// 故意先传第 1 片再传第 0 片，测试乱序重传鲁棒性
		if _, err := svc.WriteChunk(relID, upID, 1, fullData[mid:]); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.WriteChunk(relID, upID, 0, fullData[:mid]); err != nil {
			t.Fatal(err)
		}
		relPath, sz, gotHash, _, err := svc.CompleteUpload(relID, upID)
		if err != nil || sz != int64(len(fullData)) || gotHash != sumHex {
			t.Fatalf("CompleteUpload 失败: %v", err)
		}
		lastRelPath = relPath
	}

	// 验证 HTTP Range 断点续传返回 206 Partial Content
	req := httptest.NewRequest(http.MethodGet, "/download", nil)
	req.Header.Set("Range", "bytes=10-29")
	w := httptest.NewRecorder()
	if err := svc.ServeFileWithRange(w, req, lastRelPath, "bundle.tar.gz"); err != nil {
		t.Fatalf("ServeFileWithRange 失败: %v", err)
	}
	if w.Code != http.StatusPartialContent || w.Body.Len() != 20 {
		t.Fatalf("期望 206 及 20 字节分片，实际状态 %d 长度 %d", w.Code, w.Body.Len())
	}

	// 第 1 个版本应因 ArtifactKeepCount=2 自动淘汰
	wOld := httptest.NewRecorder()
	if err := svc.ServeFileWithRange(wOld, httptest.NewRequest(http.MethodGet, "/download", nil), "seccenter/release-1-v0.1.0.tar.gz", "old.tar.gz"); err == nil {
		t.Fatalf("超出保留份数的旧制品包未被清理")
	}
}
