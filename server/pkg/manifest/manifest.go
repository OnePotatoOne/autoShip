// Package manifest 定义发布制品包清单（Manifest）、迁移风险等级枚举、tar.gz 打包解包与 Ed25519 签名验签逻辑（被 Server、开发 Agent 与更新 Agent 共同引用）。
package manifest

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"autoship/pkg/crypto"
	"autoship/pkg/envelope"
	"autoship/pkg/semver"
)

// MigrationRisk 表示数据库表结构与数据迁移的风险分级（8.7 与 9.5 节）。
type MigrationRisk string

const (
	// RiskNone 表示无任何表结构或数据迁移。
	RiskNone MigrationRisk = "none"
	// RiskAdditive 表示仅新增表、新增可空/带默认值列或新增索引，可安全无损回滚。
	RiskAdditive MigrationRisk = "additive"
	// RiskDataRewrite 表示包含存量数据改写，部署前必须先执行数据库备份（9.5 节）。
	RiskDataRewrite MigrationRisk = "data_rewrite"
	// RiskDestructive 表示包含删除表/列、收窄类型等破坏性变更，默认阻断发布，须人工单独审批。
	RiskDestructive MigrationRisk = "destructive"
)

// FileEntry 描述制品包内单个文件（主程序二进制或产品部署钩子）的名称、字节大小与 SHA-256 哈希。
type FileEntry struct {
	Name   string `json:"name"`   // 包内相对文件名（如 "bin/app" 或 "hook/deploy-hook"）
	Role   string `json:"role"`   // 文件角色："binary"（业务程序或归档）或 "hook"（部署钩子脚本/程序）
	Size   int64  `json:"size"`   // 文件字节大小
	SHA256 string `json:"sha256"` // 文件内容的小写十六进制 SHA-256 摘要
}

// ReleaseManifest 是随制品包一同发布并由 Mac 钥匙串中 Ed25519 私钥签名的权威清单（8.10 第 7 步）。
type ReleaseManifest struct {
	SchemaVersion   int           `json:"schemaVersion"`   // 清单协议版本，固定为 1
	ProductCode     string        `json:"productCode"`     // 产品唯一标识编码
	Version         string        `json:"version"`         // 发布语义化版本号（如 v1.2.0）
	Tag             string        `json:"tag"`             // Git Tag 名称
	CommitSHA       string        `json:"commitSha"`       // 对应主分支提交完整哈希
	Changelog       string        `json:"changelog"`       // 本版本变更日志正文
	MigrationRisk   MigrationRisk `json:"migrationRisk"`   // 迁移风险等级：none | additive | data_rewrite | destructive
	MinAgentVersion string        `json:"minAgentVersion"` // 要求最低更新 Agent 版本（如 v0.1.0）
	CreatedAt       int64         `json:"createdAt"`       // 打包秒级 Unix 时间戳
	Files           []FileEntry   `json:"files"`           // 包内受保护文件哈希列表
	SignatureB64    string        `json:"signatureB64"`    // Ed25519 私钥对除本字段外规范化 JSON 的签名
}

// CanonicalBytes 生成用于签名与验签的确定性字节序列（置空 SignatureB64 后序列化）。
// 无入参；返回确定性 JSON 字节及序列化错误。
func (m ReleaseManifest) CanonicalBytes() ([]byte, error) {
	cp := m
	cp.SignatureB64 = ""
	return json.Marshal(cp)
}

// Sign 使用发布签名 Ed25519 私钥对清单内容进行签名并写入 SignatureB64。
// 参数 releasePrivB64 为 Base64 编码的 Ed25519 私钥；成功返回 nil，失败返回错误。
func (m *ReleaseManifest) Sign(releasePrivB64 string) error {
	if err := m.ValidateStructure(); err != nil {
		return err
	}
	payload, err := m.CanonicalBytes()
	if err != nil {
		return fmt.Errorf("生成清单签名原文失败: %w", err)
	}
	sig, err := crypto.SignBytes(releasePrivB64, payload)
	if err != nil {
		return fmt.Errorf("签名发布清单失败: %w", err)
	}
	m.SignatureB64 = sig
	return nil
}

// Verify 使用发布签名 Ed25519 公钥校验清单完整性与真实性。
// 参数 releasePubB64 为 Base64 编码的 Ed25519 公钥；验签通过且结构合法返回 nil，否则返回错误。
func (m ReleaseManifest) Verify(releasePubB64 string) error {
	if err := m.ValidateStructure(); err != nil {
		return err
	}
	if strings.TrimSpace(m.SignatureB64) == "" {
		return errors.New("发布清单缺少数字签名")
	}
	payload, err := m.CanonicalBytes()
	if err != nil {
		return err
	}
	return crypto.VerifyBytes(releasePubB64, payload, m.SignatureB64)
}

// ValidateStructure 检查清单必填项、标识符安全字符集、语义化版本号及文件项合法性。
// 无入参；合法返回 nil，非法返回错误。
func (m ReleaseManifest) ValidateStructure() error {
	if m.SchemaVersion != 1 {
		return fmt.Errorf("不支持的清单协议版本: %d", m.SchemaVersion)
	}
	if err := envelope.ValidateSafeID(m.ProductCode); err != nil {
		return fmt.Errorf("清单产品编码非法: %w", err)
	}
	if _, err := semver.Parse(m.Version); err != nil {
		return fmt.Errorf("清单版本号非法: %w", err)
	}
	if strings.TrimSpace(m.CommitSHA) == "" || strings.TrimSpace(m.Tag) == "" {
		return errors.New("清单缺少 Tag 或 CommitSHA")
	}
	switch m.MigrationRisk {
	case RiskNone, RiskAdditive, RiskDataRewrite, RiskDestructive:
	default:
		return fmt.Errorf("未知的迁移风险等级: %q", m.MigrationRisk)
	}
	if len(m.Files) == 0 {
		return errors.New("清单文件列表不能为空")
	}
	hasHook := false
	for _, f := range m.Files {
		clean := filepath.ToSlash(filepath.Clean(f.Name))
		if strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") || clean == "." {
			return fmt.Errorf("清单包含不安全的文件路径: %q", f.Name)
		}
		if len(f.SHA256) != 64 {
			return fmt.Errorf("文件 %s 的 SHA-256 长度非法", f.Name)
		}
		if f.Role == "hook" {
			hasHook = true
		}
	}
	if !hasHook {
		return errors.New("制品包清单必须包含 role=hook 的产品部署钩子")
	}
	return nil
}

// HashFile 计算磁盘文件的字节大小与小写十六进制 SHA-256 哈希。
// 参数 path 为文件路径；返回文件字节数、64 字符 hex 摘要及 IO 错误。
func HashFile(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", fmt.Errorf("打开文件计算哈希失败: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", fmt.Errorf("读取文件计算哈希失败: %w", err)
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// PackBundle 将业务制品文件、部署钩子文件与已签名的 manifest.json 打包成标准 .tar.gz 制品包。
// 参数 outputTarGz 为输出包路径，manifestObj 为已填好基本信息（会自动重算 Files 并签名）的清单指针，binaryPath 为业务制品路径，hookPath 为部署钩子脚本/程序路径，releasePrivB64 为签名私钥；成功返回总大小与整包 SHA-256。
func PackBundle(outputTarGz string, manifestObj *ReleaseManifest, binaryPath, hookPath, releasePrivB64 string) (int64, string, error) {
	binSize, binHash, err := HashFile(binaryPath)
	if err != nil {
		return 0, "", fmt.Errorf("计算制品哈希失败: %w", err)
	}
	hookSize, hookHash, err := HashFile(hookPath)
	if err != nil {
		return 0, "", fmt.Errorf("计算部署钩子哈希失败: %w", err)
	}

	binEntryName := "payload/" + filepath.Base(binaryPath)
	hookEntryName := "hook/" + filepath.Base(hookPath)
	manifestObj.SchemaVersion = 1
	manifestObj.Files = []FileEntry{
		{Name: binEntryName, Role: "binary", Size: binSize, SHA256: binHash},
		{Name: hookEntryName, Role: "hook", Size: hookSize, SHA256: hookHash},
	}
	if err := manifestObj.Sign(releasePrivB64); err != nil {
		return 0, "", err
	}

	manifestBytes, err := json.MarshalIndent(manifestObj, "", "  ")
	if err != nil {
		return 0, "", err
	}

	if err := os.MkdirAll(filepath.Dir(outputTarGz), 0755); err != nil {
		return 0, "", err
	}
	outFile, err := os.OpenFile(outputTarGz, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return 0, "", err
	}
	defer outFile.Close()

	gz := gzip.NewWriter(outFile)
	tw := tar.NewWriter(gz)

	if err := writeTarBytes(tw, "manifest.json", manifestBytes, 0644); err != nil {
		return 0, "", err
	}
	if err := writeTarFile(tw, binEntryName, binaryPath, 0755); err != nil {
		return 0, "", err
	}
	if err := writeTarFile(tw, hookEntryName, hookPath, 0755); err != nil {
		return 0, "", err
	}

	if err := tw.Close(); err != nil {
		return 0, "", err
	}
	if err := gz.Close(); err != nil {
		return 0, "", err
	}
	if err := outFile.Close(); err != nil {
		return 0, "", err
	}
	return HashFile(outputTarGz)
}

// UnpackAndVerifyBundle 解压 .tar.gz 制品包到指定安全目录，并用发布公钥严格核验 manifest.json 签名及包内每个文件的字节数与 SHA-256 摘要（任何未声明文件或路径穿越均立即失败）。
// 参数 bundleTarGz 为制品包路径，destDir 为目标解压目录，releasePubB64 为发布签名公钥；返回验证通过的 ReleaseManifest、业务制品落地路径、部署钩子落地路径及错误。
func UnpackAndVerifyBundle(bundleTarGz, destDir, releasePubB64 string) (*ReleaseManifest, string, string, error) {
	if err := os.MkdirAll(destDir, 0700); err != nil {
		return nil, "", "", fmt.Errorf("创建解压目录失败: %w", err)
	}
	f, err := os.Open(bundleTarGz)
	if err != nil {
		return nil, "", "", fmt.Errorf("打开制品包失败: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, "", "", fmt.Errorf("读取 gzip 流失败: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var parsedManifest *ReleaseManifest
	extracted := make(map[string]string)

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, "", "", fmt.Errorf("读取 tar 条目失败: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		cleanName := filepath.ToSlash(filepath.Clean(hdr.Name))
		if strings.HasPrefix(cleanName, "../") || strings.HasPrefix(cleanName, "/") || cleanName == "." {
			return nil, "", "", fmt.Errorf("拒绝制品包内路径穿越条目: %q", hdr.Name)
		}
		if cleanName == "manifest.json" {
			raw, readErr := io.ReadAll(io.LimitReader(tr, 2*1024*1024))
			if readErr != nil {
				return nil, "", "", readErr
			}
			var m ReleaseManifest
			if jsonErr := json.Unmarshal(raw, &m); jsonErr != nil {
				return nil, "", "", fmt.Errorf("解析 manifest.json 失败: %w", jsonErr)
			}
			parsedManifest = &m
			continue
		}

		targetPath := filepath.Join(destDir, filepath.FromSlash(cleanName))
		if !strings.HasPrefix(filepath.Clean(targetPath)+string(os.PathSeparator), filepath.Clean(destDir)+string(os.PathSeparator)) {
			return nil, "", "", fmt.Errorf("解压路径越界: %q", hdr.Name)
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0700); err != nil {
			return nil, "", "", err
		}
		out, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0700)
		if err != nil {
			return nil, "", "", err
		}
		_, copyErr := io.Copy(out, tr)
		_ = out.Close()
		if copyErr != nil {
			return nil, "", "", copyErr
		}
		extracted[cleanName] = targetPath
	}

	if parsedManifest == nil {
		return nil, "", "", errors.New("制品包缺少 manifest.json")
	}
	if err := parsedManifest.Verify(releasePubB64); err != nil {
		return nil, "", "", fmt.Errorf("制品包清单签名校验不通过: %w", err)
	}
	if len(extracted) != len(parsedManifest.Files) {
		return nil, "", "", fmt.Errorf("制品包包含未在签名清单中声明的额外文件: 实际 %d，清单 %d", len(extracted), len(parsedManifest.Files))
	}

	var binaryPath, hookPath string
	for _, entry := range parsedManifest.Files {
		cleanName := filepath.ToSlash(filepath.Clean(entry.Name))
		localPath, ok := extracted[cleanName]
		if !ok {
			return nil, "", "", fmt.Errorf("制品包缺失清单声明文件: %s", entry.Name)
		}
		sz, sum, err := HashFile(localPath)
		if err != nil {
			return nil, "", "", err
		}
		if sz != entry.Size || !strings.EqualFold(sum, entry.SHA256) {
			return nil, "", "", fmt.Errorf("文件 %s 大小或 SHA-256 与签名清单不一致", entry.Name)
		}
		switch entry.Role {
		case "binary":
			binaryPath = localPath
		case "hook":
			hookPath = localPath
		}
	}
	return parsedManifest, binaryPath, hookPath, nil
}

// writeTarBytes 向 tar 归档流写入一段内存字节条目；参数 tw 为 tar 写入器，name 为包内路径，content 为内容，mode 为权限位；返回写入错误，由 PackBundle 内部调用。
func writeTarBytes(tw *tar.Writer, name string, content []byte, mode int64) error {
	hdr := &tar.Header{
		Name: name,
		Mode: mode,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(content)
	return err
}

// writeTarFile 将本地磁盘文件写入 tar 归档流；参数 tw 为 tar 写入器，entryName 为包内相对名，srcPath 为源文件路径，mode 为权限位；返回 IO 错误，由 PackBundle 内部调用。
func writeTarFile(tw *tar.Writer, entryName, srcPath string, mode int64) error {
	info, err := os.Stat(srcPath)
	if err != nil {
		return err
	}
	hdr := &tar.Header{
		Name: entryName,
		Mode: mode,
		Size: info.Size(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}
