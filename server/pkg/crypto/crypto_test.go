// Package crypto_test 验证加密、签名、HMAC 与 RFC 6238 TOTP 工具的正确性与防篡改性。
package crypto_test

import (
	"testing"
	"time"

	"autoship/pkg/crypto"
)

// TestEncryptDecryptField 验证敏感配置字段的 AES-256-GCM 加解密闭环与幂等前缀行为。
func TestEncryptDecryptField(t *testing.T) {
	key := crypto.DeriveKey([]byte("unit-test-master-salt"))
	enc, err := crypto.EncryptField("super-secret-value", key)
	if err != nil {
		t.Fatalf("EncryptField 失败: %v", err)
	}
	if enc == "super-secret-value" {
		t.Fatalf("加密后仍为明文")
	}
	dec, err := crypto.DecryptField(enc, key)
	if err != nil {
		t.Fatalf("DecryptField 失败: %v", err)
	}
	if dec != "super-secret-value" {
		t.Fatalf("解密值不一致: got %q", dec)
	}
}

// TestEd25519AndOpenAPISigning 验证 Agent Ed25519 请求签名与 Open API HMAC-SHA256 签名及过期拦截。
func TestEd25519AndOpenAPISigning(t *testing.T) {
	pub, priv, err := crypto.GenerateEd25519KeyPair()
	if err != nil {
		t.Fatalf("生成密钥对失败: %v", err)
	}
	now := time.Unix(1795000000, 0)
	ts := "1795000000"
	body := []byte(`{"hello":"world"}`)

	sig, err := crypto.SignHTTPRequestEd25519(priv, "POST", "/agent/v1/dev/events?a=1", ts, "nonce-123", body)
	if err != nil {
		t.Fatalf("签名失败: %v", err)
	}
	if err := crypto.VerifyHTTPRequestEd25519(pub, "POST", "/agent/v1/dev/events?a=1", ts, "nonce-123", body, sig, now, 5*time.Minute); err != nil {
		t.Fatalf("合法签名校验失败: %v", err)
	}
	if err := crypto.VerifyHTTPRequestEd25519(pub, "POST", "/agent/v1/dev/events?a=1", ts, "nonce-123", []byte(`{"tampered":true}`), sig, now, 5*time.Minute); err == nil {
		t.Fatalf("篡改请求体后仍通过验签")
	}
	if err := crypto.VerifyHTTPRequestEd25519(pub, "POST", "/agent/v1/dev/events?a=1", ts, "nonce-123", body, sig, now.Add(10*time.Minute), 5*time.Minute); err == nil {
		t.Fatalf("过期请求仍通过验签")
	}

	hmacSig := crypto.SignOpenAPIRequest("app-secret-1", "POST", "/open/v1/conversations", ts, "n1", body)
	if err := crypto.VerifyOpenAPIRequest("app-secret-1", "POST", "/open/v1/conversations", ts, "n1", body, hmacSig, now, 5*time.Minute); err != nil {
		t.Fatalf("合法 Open API 验签失败: %v", err)
	}
	if err := crypto.VerifyOpenAPIRequest("wrong-secret", "POST", "/open/v1/conversations", ts, "n1", body, hmacSig, now, 5*time.Minute); err == nil {
		t.Fatalf("错误密钥仍通过 Open API 验签")
	}
}

// TestOTPAndRecovery 验证 6 位动态口令窗口校验、防重放步数递增以及恢复码哈希。
func TestOTPAndRecovery(t *testing.T) {
	secret, err := crypto.NewOTPSecret()
	if err != nil {
		t.Fatalf("NewOTPSecret 失败: %v", err)
	}
	now := time.Unix(1795000020, 0)
	code, err := crypto.OTPCode(secret, now.Unix()/30)
	if err != nil {
		t.Fatalf("OTPCode 失败: %v", err)
	}
	step, ok := crypto.ValidateOTP(secret, code, now, 0)
	if !ok {
		t.Fatalf("合法 OTP 未通过校验")
	}
	if _, okReplay := crypto.ValidateOTP(secret, code, now, step); okReplay {
		t.Fatalf("同一时间步的 OTP 重放未被拒绝")
	}
	codes, hashes, err := crypto.NewRecoveryCodes()
	if err != nil || len(codes) != 8 || len(hashes) != 8 {
		t.Fatalf("生成恢复码异常: %v", err)
	}
	if crypto.RecoveryCodeHash(codes[0]) != hashes[0] {
		t.Fatalf("恢复码哈希不一致")
	}
}

// TestSimplePasswordHashAndVerify 验证不限制复杂度的简单口令（如 123456）能够正常计算哈希与比对。
// 参数 t 为 Go 标准测试上下文；无返回值。
func TestSimplePasswordHashAndVerify(t *testing.T) {
	hash, err := crypto.HashPassword("123456")
	if err != nil {
		t.Fatalf("HashPassword(123456) 失败: %v", err)
	}
	if !crypto.CheckPassword(hash, "123456") {
		t.Fatalf("CheckPassword(123456) 期望匹配成功")
	}
	if crypto.CheckPassword(hash, "654321") {
		t.Fatalf("错误口令不应匹配通过")
	}
}
