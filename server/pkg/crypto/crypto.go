// Package crypto 提供 autoShip 平台通用的对称加密、密码哈希、Ed25519 非对称签名、Open API HMAC 签名及 RFC 6238 TOTP 动态口令工具。
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// appSeed 是与部署实例主盐值混合的内置种子，用于派生配置加密密钥。
var appSeed = []byte("AutoShip@2026!EncKey#Seed")

// DeriveKey 根据传入的主盐值派生 32 字节的 AES-256 密钥。
// 参数 masterSalt 为配置文件或环境变量提供的主盐字节切片；返回 32 字节密钥，供 EncryptField 与 DecryptField 调用。
func DeriveKey(masterSalt []byte) []byte {
	h := sha256.New()
	_, _ = h.Write(appSeed)
	_, _ = h.Write(masterSalt)
	return h.Sum(nil)
}

// Encrypt 使用 AES-256-GCM 对明文字符串进行认证加密。
// 参数 plaintext 为待加密明文，key 为 32 字节密钥；返回 Base64 编码的密文（含随机 Nonce）或错误信息，通过 Encrypt(plain, key) 调用。
func Encrypt(plaintext string, key []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("初始化 AES 密码块失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("初始化 GCM 模式失败: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("生成加密随机 Nonce 失败: %w", err)
	}
	// 将随机 Nonce 拼在密文头部，解密时按固定长度切分
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt 解密由 Encrypt 生成的 Base64 编码 AES-256-GCM 密文。
// 参数 encoded 为 Base64 密文，key 为 32 字节密钥；返回还原后的明文或验签解密错误，通过 Decrypt(encoded, key) 调用。
func Decrypt(encoded string, key []byte) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("解码 Base64 密文失败: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("初始化 AES 密码块失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("初始化 GCM 模式失败: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("密文长度不足，无法提取 Nonce")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("GCM 认证解密失败: %w", err)
	}
	return string(plaintext), nil
}

// EncryptField 为敏感配置或数据库字段加密并附加 "ENC:" 前缀。
// 参数 value 为明文字符串（空串原样返回，已有 "ENC:" 前缀则不重复加密），key 为 32 字节密钥；返回带前缀密文及错误，供持久化前调用。
func EncryptField(value string, key []byte) (string, error) {
	if value == "" || strings.HasPrefix(value, "ENC:") {
		return value, nil
	}
	enc, err := Encrypt(value, key)
	if err != nil {
		return "", err
	}
	return "ENC:" + enc, nil
}

// DecryptField 解密带 "ENC:" 前缀的字段值；若无该前缀则按兼容明文原样返回。
// 参数 value 为存储字段内容，key 为 32 字节密钥；返回明文字符串及错误，供服务层读取密钥时调用。
func DecryptField(value string, key []byte) (string, error) {
	if !strings.HasPrefix(value, "ENC:") {
		return value, nil
	}
	return Decrypt(strings.TrimPrefix(value, "ENC:"), key)
}

// GenerateRandomHex 生成指定字节数的密码学安全随机十六进制字符串。
// 参数 numBytes 为随机字节长度（必须大于 0）；返回长度为 2*numBytes 的小写 hex 字符串及错误，供生成令牌、Nonce 和盐值调用。
func GenerateRandomHex(numBytes int) (string, error) {
	if numBytes <= 0 {
		return "", errors.New("随机字节长度必须大于 0")
	}
	buf := make([]byte, numBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("读取安全随机源失败: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// HashPassword 使用 bcrypt（cost=12）计算用户口令的慢哈希摘要。
// 参数 password 为明文口令；返回 bcrypt 哈希字符串及错误，供创建管理员或修改密码时调用。
func HashPassword(password string) (string, error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return "", fmt.Errorf("计算口令哈希失败: %w", err)
	}
	return string(hashed), nil
}

// CheckPassword 校验明文口令与已存储的 bcrypt 哈希是否匹配。
// 参数 hash 为数据库存储的哈希串，password 为用户提交的口令；匹配返回 true，否则返回 false，供登录服务调用。
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// GenerateEd25519KeyPair 生成一对新的 Ed25519 公私钥，并以 Base64 标准编码返回。
// 无入参；返回 pubBase64（32 字节公钥编码）、privBase64（64 字节私钥编码）及错误，供 Agent 首次启动或发布密钥初始化调用。
func GenerateEd25519KeyPair() (pubBase64 string, privBase64 string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("生成 Ed25519 密钥对失败: %w", err)
	}
	return base64.StdEncoding.EncodeToString(pub), base64.StdEncoding.EncodeToString(priv), nil
}

// ParseEd25519PublicKey 解析 Base64 编码的 Ed25519 公钥。
// 参数 pubBase64 为 Base64 编码公钥字符串；返回标准 ed25519.PublicKey 及错误，供验签逻辑调用。
func ParseEd25519PublicKey(pubBase64 string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pubBase64))
	if err != nil {
		return nil, fmt.Errorf("解码 Ed25519 公钥 Base64 失败: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("Ed25519 公钥长度非法: 期望 %d 字节，实际 %d 字节", ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}

// ParseEd25519PrivateKey 解析 Base64 编码的 Ed25519 私钥（支持 64 字节完整私钥或 32 字节种子）。
// 参数 privBase64 为 Base64 私钥字符串；返回标准 ed25519.PrivateKey 及错误，供 Agent 请求签名与发布签名调用。
func ParseEd25519PrivateKey(privBase64 string) (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(privBase64))
	if err != nil {
		return nil, fmt.Errorf("解码 Ed25519 私钥 Base64 失败: %w", err)
	}
	if len(raw) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(raw), nil
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("Ed25519 私钥长度非法: 期望 %d 字节，实际 %d 字节", ed25519.PrivateKeySize, len(raw))
	}
	return ed25519.PrivateKey(raw), nil
}

// SignBytes 使用 Base64 编码的 Ed25519 私钥对任意字节载荷生成分离签名。
// 参数 privBase64 为私钥，payload 为待签名字节；返回 Base64 签名字符串及错误，供制品清单和动作票据签名调用。
func SignBytes(privBase64 string, payload []byte) (string, error) {
	priv, err := ParseEd25519PrivateKey(privBase64)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, payload)
	return base64.StdEncoding.EncodeToString(sig), nil
}

// VerifyBytes 使用 Base64 编码的 Ed25519 公钥校验字节载荷的分离签名。
// 参数 pubBase64 为公钥，payload 为原始字节，sigBase64 为 Base64 签名；验签通过返回 nil，失败返回具体错误。
func VerifyBytes(pubBase64 string, payload []byte, sigBase64 string) error {
	pub, err := ParseEd25519PublicKey(pubBase64)
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigBase64))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("签名格式或长度非法")
	}
	if !ed25519.Verify(pub, payload, sig) {
		return errors.New("Ed25519 签名校验不通过")
	}
	return nil
}

// BuildCanonicalRequestPayload 构造 HTTP 请求签名的规范化原文（覆盖方法、含查询串路径、时间戳、随机数与请求体 SHA-256 摘要）。
// 参数 method 为大写 HTTP 方法，pathWithQuery 为请求路径及原始查询参数，timestamp 为秒级 Unix 时间戳字符串，nonce 为防重放随机数，body 为原始请求体字节；返回规范化字节切片。
func BuildCanonicalRequestPayload(method, pathWithQuery, timestamp, nonce string, body []byte) []byte {
	bodySum := sha256.Sum256(body)
	canonical := strings.Join([]string{
		strings.ToUpper(strings.TrimSpace(method)),
		pathWithQuery,
		strings.TrimSpace(timestamp),
		strings.TrimSpace(nonce),
		hex.EncodeToString(bodySum[:]),
	}, "\n")
	return []byte(canonical)
}

// SignHTTPRequestEd25519 使用 Ed25519 私钥对 HTTP 请求五要素生成签名（用于 Agent 与 Server 双向请求鉴权）。
// 参数 privBase64 为签名方私钥，method/pathWithQuery/timestamp/nonce/body 为请求五要素；返回 Base64 编码签名及错误。
func SignHTTPRequestEd25519(privBase64, method, pathWithQuery, timestamp, nonce string, body []byte) (string, error) {
	canonical := BuildCanonicalRequestPayload(method, pathWithQuery, timestamp, nonce, body)
	return SignBytes(privBase64, canonical)
}

// VerifyHTTPRequestEd25519 校验 HTTP 请求的 Ed25519 签名及时间窗合法性。
// 参数 pubBase64 为对端公钥，method/pathWithQuery/timestamp/nonce/body 为请求五要素，sigBase64 为请求头签名，now 为当前时刻，maxSkew 为允许的最大时钟偏差（如 5 分钟）；成功返回 nil，否则返回错误。
func VerifyHTTPRequestEd25519(pubBase64, method, pathWithQuery, timestamp, nonce string, body []byte, sigBase64 string, now time.Time, maxSkew time.Duration) error {
	if err := validateTimestampWindow(timestamp, now, maxSkew); err != nil {
		return err
	}
	if strings.TrimSpace(nonce) == "" {
		return errors.New("缺少防重放随机数 Nonce")
	}
	canonical := BuildCanonicalRequestPayload(method, pathWithQuery, timestamp, nonce, body)
	return VerifyBytes(pubBase64, canonical, sigBase64)
}

// SignOpenAPIRequest 使用接入方 App Secret 对 Open API 请求五要素计算 HMAC-SHA256 十六进制签名。
// 参数 secret 为接入方密钥，method/pathWithQuery/timestamp/nonce/body 为请求五要素；返回小写 hex 签名字符串，供客户端或测试代理调用。
func SignOpenAPIRequest(secret, method, pathWithQuery, timestamp, nonce string, body []byte) string {
	canonical := BuildCanonicalRequestPayload(method, pathWithQuery, timestamp, nonce, body)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(canonical)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyOpenAPIRequest 校验 Open API 请求的 HMAC-SHA256 签名与 5 分钟时间窗。
// 参数 secret 为接入方密钥，method/pathWithQuery/timestamp/nonce/body 为请求五要素，sigHex 为请求头签名，now 为当前时刻，maxSkew 为最大时间偏差；验签通过返回 nil，否则返回错误。
func VerifyOpenAPIRequest(secret, method, pathWithQuery, timestamp, nonce string, body []byte, sigHex string, now time.Time, maxSkew time.Duration) error {
	if err := validateTimestampWindow(timestamp, now, maxSkew); err != nil {
		return err
	}
	if strings.TrimSpace(nonce) == "" {
		return errors.New("缺少防重放随机数 Nonce")
	}
	expected := SignOpenAPIRequest(secret, method, pathWithQuery, timestamp, nonce, body)
	provided := strings.ToLower(strings.TrimSpace(sigHex))
	if subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) != 1 {
		return errors.New("Open API HMAC 签名校验不通过")
	}
	return nil
}

// validateTimestampWindow 检查请求头秒级时间戳是否落在当前时间允许的窗口内。
// 参数 tsStr 为秒级 Unix 时间戳文本，now 为服务端当前时间，maxSkew 为允许偏差；超出窗口或格式非法时返回错误。
func validateTimestampWindow(tsStr string, now time.Time, maxSkew time.Duration) error {
	sec, err := strconv.ParseInt(strings.TrimSpace(tsStr), 10, 64)
	if err != nil {
		return errors.New("请求时间戳格式非法")
	}
	reqTime := time.Unix(sec, 0)
	diff := now.Sub(reqTime)
	if diff < 0 {
		diff = -diff
	}
	if diff > maxSkew {
		return fmt.Errorf("请求时间戳超出允许范围（最大偏差 %s）", maxSkew)
	}
	return nil
}

// NewOTPSecret 生成每管理员账号独立的 160 位无填充 Base32 TOTP 密钥（遵循 RFC 4226 / RFC 6238）。
// 无入参；返回 Base32 密钥字符串与随机源错误，供管理员绑定双因素认证时调用。
func NewOTPSecret() (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("生成 OTP 密钥失败: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw), nil
}

// OTPURI 生成兼容主流身份验证器（Google Authenticator / Authy 等）的 otpauth 绑定链接。
// 参数 secret 为 Base32 密钥，username 为管理员账号名；返回标准 otpauth://totp/ URI 字符串，供前端本地绘制二维码。
func OTPURI(secret, username string) string {
	values := url.Values{
		"secret":    {secret},
		"issuer":    {"autoShip"},
		"algorithm": {"SHA1"},
		"digits":    {"6"},
		"period":    {"30"},
	}
	return "otpauth://totp/" + url.PathEscape("autoShip:"+username) + "?" + values.Encode()
}

// OTPCode 计算给定 Base32 密钥与 30 秒时间步计数器的 6 位动态口令。
// 参数 secret 为无填充 Base32 密钥，step 为非负时间计数器（unixSec / 30）；返回固定 6 位数字字符串及错误。
func OTPCode(secret string, step int64) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil || len(key) < 20 || step < 0 {
		return "", errors.New("无效的 OTP 密钥或时间步")
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(counter[:])
	digest := mac.Sum(nil)
	// 按 RFC 4226 动态截断计算 6 位十进制验证码
	offset := digest[len(digest)-1] & 0x0f
	number := binary.BigEndian.Uint32(digest[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", number%1000000), nil
}

// ValidateOTP 校验用户提交的 6 位动态口令（允许前后各 1 个 30 秒时间步误差，并拒绝已消费过的时间步以防重放）。
// 参数 secret 为 Base32 密钥，code 为用户输入的 6 位数字，now 为验证时刻，lastStep 为数据库中已记录的最大消费时间步；返回匹配的时间步与是否通过布尔值。
func ValidateOTP(secret, code string, now time.Time, lastStep int64) (int64, bool) {
	if len(code) != 6 {
		return 0, false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	matched := int64(-1)
	for delta := int64(-1); delta <= 1; delta++ {
		step := now.Unix()/30 + delta
		expected, err := OTPCode(secret, step)
		if err == nil && step > lastStep && subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			matched = step
		}
	}
	return matched, matched >= 0
}

// RecoveryCodeHash 计算一次性恢复码的带域分离 SHA-256 不可逆摘要。
// 参数 code 为恢复码输入（自动忽略横线分隔符与大小写差异）；返回十六进制摘要字符串，供持久化与恒定时间比对使用。
func RecoveryCodeHash(code string) string {
	canonical := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	sum := sha256.Sum256([]byte("autoship-otp-recovery-v1:" + canonical))
	return hex.EncodeToString(sum[:])
}

// NewRecoveryCodes 一次性生成 8 个 128 位高熵恢复码及其对应哈希列表。
// 无入参；返回明文恢复码切片（仅在绑定完成时向本人展示一次）、摘要切片（写入数据库）及错误。
func NewRecoveryCodes() (codes []string, hashes []string, err error) {
	for i := 0; i < 8; i++ {
		buf := make([]byte, 16)
		if _, err := rand.Read(buf); err != nil {
			return nil, nil, fmt.Errorf("生成恢复码失败: %w", err)
		}
		raw := hex.EncodeToString(buf)
		formatted := raw[:8] + "-" + raw[8:16] + "-" + raw[16:24] + "-" + raw[24:]
		codes = append(codes, formatted)
		hashes = append(hashes, RecoveryCodeHash(formatted))
	}
	return codes, hashes, nil
}
