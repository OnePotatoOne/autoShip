// Package keychain 封装 macOS 系统钥匙串（/usr/bin/security）凭据存取接口，并提供内存实现供自动化单元测试与非 macOS 环境注入。
package keychain

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// ErrSecretNotFound 表示目标服务名与账号对应的密钥尚未存入钥匙串。
var ErrSecretNotFound = errors.New("钥匙串中未找到指定凭据")

// Store 抽象开发 Agent 读取与保存模型 API Key、GitHub PAT 及发布签名 Ed25519 私钥的安全凭据仓库。
type Store interface {
	// Get 按服务名与条目名读取机密文本；不存在时返回 ErrSecretNotFound。
	Get(service, account string) (string, error)
	// Set 按服务名与条目名写入或更新机密文本。
	Set(service, account, secret string) error
	// Delete 删除指定的机密条目；条目不存在时不视为错误。
	Delete(service, account string) error
}

// MacOSKeychain 使用 macOS 原生 `/usr/bin/security` 命令行访问当前登录用户的默认钥匙串（8.1 与 10 节要求）。
type MacOSKeychain struct{}

// NewDefaultStore 根据当前运行操作系统返回默认凭据存储：darwin 环境返回 MacOSKeychain，其他平台返回进程内安全内存存储。
// 无入参；返回 Store 接口实例，供开发 Agent 启动阶段初始化调用。
func NewDefaultStore() Store {
	if runtime.GOOS == "darwin" {
		return &MacOSKeychain{}
	}
	return NewMemoryStore()
}

// Get 从 macOS 用户钥匙串读取通用密码条目。
// 参数 service 为钥匙串服务标识（如 "autoship.release.signing"），account 为账号键名；返回密码明文或 ErrSecretNotFound/执行错误。
func (k *MacOSKeychain) Get(service, account string) (string, error) {
	cmd := exec.Command("/usr/bin/security", "find-generic-password", "-s", service, "-a", account, "-w")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errText := stderr.String()
		if strings.Contains(errText, "could not be found") || strings.Contains(errText, "SecKeychainSearchCopyNext") {
			return "", ErrSecretNotFound
		}
		return "", fmt.Errorf("读取 macOS 钥匙串失败: %w (%s)", err, strings.TrimSpace(errText))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Set 向 macOS 用户钥匙串写入或覆盖（-U）通用密码条目。
// 参数 service 为服务标识，account 为账号键名，secret 为机密内容；成功返回 nil，失败返回错误。
func (k *MacOSKeychain) Set(service, account, secret string) error {
	cmd := exec.Command("/usr/bin/security", "add-generic-password", "-U", "-s", service, "-a", account, "-w", secret)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("写入 macOS 钥匙串失败: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Delete 从 macOS 用户钥匙串删除指定通用密码条目。
// 参数 service 为服务标识，account 为账号键名；删除成功或原本不存在均返回 nil。
func (k *MacOSKeychain) Delete(service, account string) error {
	cmd := exec.Command("/usr/bin/security", "delete-generic-password", "-s", service, "-a", account)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "could not be found") {
			return nil
		}
		return fmt.Errorf("删除 macOS 钥匙串凭据失败: %w", err)
	}
	return nil
}

// MemoryStore 是并发安全的内存凭据仓库，供单元测试及沙箱隔离演练注入，避免污染宿主真实钥匙串。
type MemoryStore struct {
	mu   sync.RWMutex
	data map[string]string
}

// NewMemoryStore 创建一个空的内存凭据存储实例。
// 无入参；返回 *MemoryStore。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{data: make(map[string]string)}
}

// Get 从内存映射中读取指定凭据。
// 参数 service/account 组成复合键；命中返回机密文本，未命中返回 ErrSecretNotFound。
func (m *MemoryStore) Get(service, account string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	val, ok := m.data[service+"::"+account]
	if !ok {
		return "", ErrSecretNotFound
	}
	return val, nil
}

// Set 将凭据保存到内存映射中。
// 参数 service/account 为复合键，secret 为机密字符串；始终返回 nil。
func (m *MemoryStore) Set(service, account, secret string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[service+"::"+account] = secret
	return nil
}

// Delete 从内存映射中移除指定凭据。
// 参数 service/account 为复合键；始终返回 nil。
func (m *MemoryStore) Delete(service, account string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, service+"::"+account)
	return nil
}
