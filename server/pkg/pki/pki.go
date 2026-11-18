// Package pki 实现 autoShip 自建根 CA、双监听器服务端证书签发、开发 Agent CSR 证书签发，以及基于固定 CA 指纹的 TLS 校验与客户端构造。
package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Bundle 保存自建根 CA 的证书、私钥及 SHA-256 指纹，供 Server 在内存中使用以签发各端叶证书。
type Bundle struct {
	CACert        *x509.Certificate // 解析后的根 CA X.509 证书对象
	CAKey         crypto.Signer     // 根 CA 私钥（仅保留在 Server 本机，绝不外发）
	CACertPEM     []byte            // 根 CA 公开证书 PEM 字节，供管理后台下载安装到个人电脑/手机
	CAFingerprint string            // 根 CA 证书 DER 字节的小写 SHA-256 十六进制指纹（64 字符）
}

// EnsureCA 确保指定目录下存在有效的 autoShip 根 CA 证书与私钥；若不存在则在首次启动时自动生成（有效期 10 年）。
// 参数 pkiDir 为证书存储目录路径；返回加载或新生成的 CA Bundle 实例及错误信息，供 Server 启动初始化调用。
func EnsureCA(pkiDir string) (*Bundle, error) {
	if err := os.MkdirAll(pkiDir, 0755); err != nil {
		return nil, fmt.Errorf("创建 PKI 目录失败: %w", err)
	}
	caCertPath := filepath.Join(pkiDir, "ca.crt")
	caKeyPath := filepath.Join(pkiDir, "ca.key")

	if fileExists(caCertPath) && fileExists(caKeyPath) {
		return LoadCA(caCertPath, caKeyPath)
	}

	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成根 CA ECDSA P-256 私钥失败: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"autoShip Platform"},
			CommonName:   "autoShip Root CA",
		},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}

	caDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &privKey.PublicKey, privKey)
	if err != nil {
		return nil, fmt.Errorf("创建根 CA 证书失败: %w", err)
	}
	caCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	if err := writeFileMode(caCertPath, caCertPEM, 0644); err != nil {
		return nil, err
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(privKey)
	if err != nil {
		return nil, fmt.Errorf("序列化根 CA 私钥失败: %w", err)
	}
	caKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := writeFileMode(caKeyPath, caKeyPEM, 0600); err != nil {
		return nil, err
	}

	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, fmt.Errorf("解析新生成的根 CA 证书失败: %w", err)
	}
	return &Bundle{
		CACert:        caCert,
		CAKey:         privKey,
		CACertPEM:     caCertPEM,
		CAFingerprint: CertDERFingerprint(caDER),
	}, nil
}

// LoadCA 从磁盘加载已有的根 CA 证书与私钥文件并校验匹配关系。
// 参数 caCertPath 为根证书路径，caKeyPath 为根私钥路径；返回 Bundle 对象及错误，供 EnsureCA 内部或证书工具调用。
func LoadCA(caCertPath, caKeyPath string) (*Bundle, error) {
	certPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("读取根 CA 证书失败: %w", err)
	}
	keyPEM, err := os.ReadFile(caKeyPath)
	if err != nil {
		return nil, fmt.Errorf("读取根 CA 私钥失败: %w", err)
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return nil, fmt.Errorf("根 CA 证书与私钥不匹配: %w", err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, errors.New("根 CA 证书 PEM 解码失败")
	}
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("解析根 CA X.509 结构失败: %w", err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, errors.New("根 CA 私钥 PEM 解码失败")
	}
	parsedKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("解析根 CA PKCS8 私钥失败: %w", err)
	}
	signer, ok := parsedKey.(crypto.Signer)
	if !ok {
		return nil, errors.New("根 CA 私钥不支持 crypto.Signer 接口")
	}
	return &Bundle{
		CACert:        caCert,
		CAKey:         signer,
		CACertPEM:     certPEM,
		CAFingerprint: CertDERFingerprint(block.Bytes),
	}, nil
}

// EnsureServerCert 确保指定名称的服务端叶证书存在、未过期且包含全部要求的 SAN IP；若不满足则自动使用根 CA 重新签发（写入含根 CA 的完整证书链，便于仅凭 CA 指纹的对端在 TLS 握手中完成链验证）。
// 参数 pkiDir 为证书目录，name 为用途标识（如 "public" 或 "tailnet"），commonName 为证书 CN，sanIPs 为需要写入 SAN 的 IP 列表，sanDNS 为可选 DNS 列表；返回 tls.Certificate 及错误。
func (b *Bundle) EnsureServerCert(pkiDir, name, commonName string, sanIPs []string, sanDNS []string) (tls.Certificate, error) {
	certPath := filepath.Join(pkiDir, name+".crt")
	keyPath := filepath.Join(pkiDir, name+".key")

	ips, err := normalizeIPs(sanIPs)
	if err != nil {
		return tls.Certificate{}, err
	}

	if fileExists(certPath) && fileExists(keyPath) {
		if pair, ok := b.loadReusableLeaf(certPath, keyPath, ips); ok {
			return pair, nil
		}
	}

	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("生成叶证书私钥失败: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"autoShip Platform"},
			CommonName:   commonName,
		},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(3, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           ips,
		DNSNames:              sanDNS,
	}

	leafDER, err := x509.CreateCertificate(rand.Reader, tmpl, b.CACert, &privKey.PublicKey, b.CAKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("签发服务端叶证书失败: %w", err)
	}
	// 将叶证书与根 CA 拼为完整链输出，客户端握手时无需预先落盘 ca.crt 即可仅凭固定 CA 指纹验证整条链
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	fullChainPEM := append(leafPEM, b.CACertPEM...)
	if err := writeFileMode(certPath, fullChainPEM, 0644); err != nil {
		return tls.Certificate{}, err
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(privKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("序列化叶证书私钥失败: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := writeFileMode(keyPath, keyPEM, 0600); err != nil {
		return tls.Certificate{}, err
	}

	return tls.X509KeyPair(fullChainPEM, keyPEM)
}

// GenerateCSR 在本地（例如 Mac 开发 Agent）生成 ECDSA P-256 私钥及对应的证书签名请求（CSR），保证叶私钥绝不离开本机。
// 参数 commonName 为证书标识，sanIPs 为希望写入 SAN 的 IP 地址列表（如 Mac 的 tailnet IP 及 127.0.0.1）；返回 csrPEM、keyPEM 及错误。
func GenerateCSR(commonName string, sanIPs []string) (csrPEM []byte, keyPEM []byte, err error) {
	ips, err := normalizeIPs(sanIPs)
	if err != nil {
		return nil, nil, err
	}
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("生成 CSR 私钥失败: %w", err)
	}
	reqTmpl := &x509.CertificateRequest{
		Subject: pkix.Name{
			Organization: []string{"autoShip Agent"},
			CommonName:   commonName,
		},
		IPAddresses: ips,
		DNSNames:    []string{"localhost"},
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, reqTmpl, privKey)
	if err != nil {
		return nil, nil, fmt.Errorf("创建 X.509 CSR 失败: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privKey)
	if err != nil {
		return nil, nil, fmt.Errorf("序列化 CSR 私钥失败: %w", err)
	}
	csrPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return csrPEM, keyPEM, nil
}

// SignCSR 在 Server 侧校验开发 Agent 提交的 CSR 自签名，并使用 autoShip 根 CA 签发服务端证书链（用于开发 Agent 模型中继 HTTPS 与本地预览实例 HTTPS）。
// 参数 csrPEM 为 Agent 提交的 PEM 编码 CSR，allowedIPs 为服务端强制覆盖/限制的合法 SAN IP 列表（如登记的 tailnet IP 与 127.0.0.1），validityDays 为有效天数；返回含根 CA 的完整证书链 PEM 及错误。
func (b *Bundle) SignCSR(csrPEM []byte, allowedIPs []string, validityDays int) ([]byte, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, errors.New("无效的 CSR PEM 内容")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("解析 CSR 结构失败: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("CSR 自身签名校验失败: %w", err)
	}
	ips, err := normalizeIPs(allowedIPs)
	if err != nil {
		return nil, err
	}
	if validityDays <= 0 || validityDays > 1095 {
		validityDays = 365
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               csr.Subject,
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(0, 0, validityDays),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IPAddresses:           ips,
		DNSNames:              []string{"localhost"},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, tmpl, b.CACert, csr.PublicKey, b.CAKey)
	if err != nil {
		return nil, fmt.Errorf("使用根 CA 签发 CSR 失败: %w", err)
	}
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	return append(leafPEM, b.CACertPEM...), nil
}

// CertDERFingerprint 计算单个 X.509 证书 DER 字节的小写 SHA-256 指纹（不含冒号，64 字符）。
// 参数 der 为证书原始字节；返回 64 位小写 hex 字符串。
func CertDERFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// NormalizeFingerprint 规范化用户配置的证书指纹字符串（去除冒号、空白及 sha256: 前缀并转小写）。
// 参数 fp 为配置文件中的指纹文本；返回标准 64 位小写 hex 指纹，格式不合法时返回错误。
func NormalizeFingerprint(fp string) (string, error) {
	cleaned := strings.ToLower(strings.TrimSpace(fp))
	cleaned = strings.TrimPrefix(cleaned, "sha256:")
	cleaned = strings.ReplaceAll(cleaned, ":", "")
	cleaned = strings.ReplaceAll(cleaned, " ", "")
	if len(cleaned) != 64 {
		return "", fmt.Errorf("CA 指纹长度非法: 期望 64 个十六进制字符，实际 %d", len(cleaned))
	}
	if _, err := hex.DecodeString(cleaned); err != nil {
		return "", fmt.Errorf("CA 指纹包含非十六进制字符: %w", err)
	}
	return cleaned, nil
}

// NewCAPinnedTLSConfig 构造固定根 CA SHA-256 指纹的客户端 TLS 配置（满足设计文档 7.4：固定 CA 指纹而不固定叶证书，服务端换 IP 或轮换叶证书时各端零改动）。
// 参数 expectedCAFingerprint 为 autoShip 根 CA 的 SHA-256 指纹，serverHost 为目标主机或 IP（用于校验叶证书 SAN，为空则仅校验证书链与有效期）；返回 *tls.Config 及错误。
func NewCAPinnedTLSConfig(expectedCAFingerprint string, serverHost string) (*tls.Config, error) {
	normFP, err := NormalizeFingerprint(expectedCAFingerprint)
	if err != nil {
		return nil, err
	}
	cleanHost := strings.TrimSpace(serverHost)
	if h, _, splitErr := net.SplitHostPort(cleanHost); splitErr == nil {
		cleanHost = h
	}

	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// 关闭系统默认根证书池验证，改由 VerifyPeerCertificate 用固定指纹的根 CA 执行严格链验证与 SAN 验证
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("服务端未提供任何 TLS 证书")
			}
			parsed := make([]*x509.Certificate, 0, len(rawCerts))
			for _, raw := range rawCerts {
				c, parseErr := x509.ParseCertificate(raw)
				if parseErr != nil {
					return fmt.Errorf("解析对端证书失败: %w", parseErr)
				}
				parsed = append(parsed, c)
			}

			// 在服务端提供的证书链中查找匹配固定指纹的根 CA
			roots := x509.NewCertPool()
			intermediates := x509.NewCertPool()
			matchedCA := false
			for idx, c := range parsed {
				fp := CertDERFingerprint(c.Raw)
				if subtle.ConstantTimeCompare([]byte(fp), []byte(normFP)) == 1 && c.IsCA {
					roots.AddCert(c)
					matchedCA = true
				} else if idx > 0 {
					intermediates.AddCert(c)
				}
			}
			if !matchedCA {
				return errors.New("服务端证书链中未找到匹配预期指纹的 autoShip 根 CA")
			}

			verifyOpts := x509.VerifyOptions{
				Roots:         roots,
				Intermediates: intermediates,
				CurrentTime:   time.Now().UTC(),
				KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			}
			if cleanHost != "" {
				verifyOpts.DNSName = cleanHost
			}
			if _, verifyErr := parsed[0].Verify(verifyOpts); verifyErr != nil {
				return fmt.Errorf("基于固定根 CA 的服务端证书校验失败: %w", verifyErr)
			}
			return nil
		},
	}, nil
}

// NewCAPinnedHTTPClient 创建绑定固定 autoShip CA 指纹与超时设置的 HTTPS 客户端。
// 参数 expectedCAFingerprint 为根 CA 指纹，serverHost 为目标 IP/主机名，timeout 为单次请求总超时；返回 *http.Client 及错误。
func NewCAPinnedHTTPClient(expectedCAFingerprint string, serverHost string, timeout time.Duration) (*http.Client, error) {
	tlsCfg, err := NewCAPinnedTLSConfig(expectedCAFingerprint, serverHost)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		TLSClientConfig:     tlsCfg,
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     60 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}, nil
}

// loadReusableLeaf 检查已有叶证书私钥是否匹配、距离过期是否大于 30 天且已包含全部所需 SAN IP。
// 参数 certPath/keyPath 为证书私钥路径，requiredIPs 为要求的 SAN IP 列表；可复用时返回 (pair, true)，否则返回 false。
func (b *Bundle) loadReusableLeaf(certPath, keyPath string, requiredIPs []net.IP) (tls.Certificate, bool) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return tls.Certificate{}, false
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return tls.Certificate{}, false
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil || len(pair.Certificate) == 0 {
		return tls.Certificate{}, false
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return tls.Certificate{}, false
	}
	now := time.Now().UTC()
	if now.Before(leaf.NotBefore) || now.AddDate(0, 0, 30).After(leaf.NotAfter) {
		return tls.Certificate{}, false
	}
	roots := x509.NewCertPool()
	roots.AddCert(b.CACert)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now}); err != nil {
		return tls.Certificate{}, false
	}
	existing := make(map[string]bool, len(leaf.IPAddresses))
	for _, ip := range leaf.IPAddresses {
		existing[ip.String()] = true
	}
	for _, reqIP := range requiredIPs {
		if !existing[reqIP.String()] {
			return tls.Certificate{}, false
		}
	}
	return pair, true
}

// normalizeIPs 去重并校验输入 IP 列表（始终补齐 127.0.0.1 与 ::1 以便本机自检与回环调试）。
// 参数 rawIPs 为配置传入的 IP 字符串切片；返回解析后的 net.IP 切片及格式错误。
func normalizeIPs(rawIPs []string) ([]net.IP, error) {
	seen := make(map[string]bool)
	var out []net.IP
	add := func(ip net.IP) {
		if ip == nil {
			return
		}
		k := ip.String()
		if !seen[k] {
			seen[k] = true
			out = append(out, ip)
		}
	}
	add(net.ParseIP("127.0.0.1"))
	add(net.ParseIP("::1"))
	for _, s := range rawIPs {
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			continue
		}
		if h, _, splitErr := net.SplitHostPort(trimmed); splitErr == nil {
			trimmed = h
		}
		ip := net.ParseIP(trimmed)
		if ip == nil {
			return nil, fmt.Errorf("证书 SAN 包含无效的 IP 地址: %s", trimmed)
		}
		add(ip)
	}
	return out, nil
}

// randomSerial 生成 128 位随机正整数作为 X.509 证书序列号。
// 无入参；返回 *big.Int 及随机源错误。
func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("生成证书序列号失败: %w", err)
	}
	return serial, nil
}

// writeFileMode 将内容写入目标路径并显式设置文件权限（避免已有旧文件保留过宽权限）。
// 参数 path 为文件路径，data 为文件内容，mode 为权限位；返回写入或 chmod 错误。
func writeFileMode(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("打开写入文件失败: %w", err)
	}
	defer f.Close()
	if err := f.Chmod(mode); err != nil {
		return fmt.Errorf("设置文件权限失败: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("写入文件内容失败: %w", err)
	}
	return nil
}

// fileExists 判断目标路径是否存在且不为目录。
// 参数 path 为文件系统路径；存在普通文件返回 true，否则返回 false。
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
