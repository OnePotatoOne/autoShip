// Package desensitize 提供文本中 IP 地址、电子邮箱、API 密钥、Bearer 令牌与数据库连接串密码的自动脱敏工具（用于客服输入交模型前脱敏及运行日志脱敏）。
package desensitize

import "regexp"

var (
	// ipv4Re 匹配常见 IPv4 地址（排除形如 v1.2.3.4 的版本号前缀干扰，仅脱敏标准四段式 IP）
	ipv4Re = regexp.MustCompile(`\b((?:25[0-5]|2[0-4]\d|1?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|1?\d?\d)\b`)
	// emailRe 匹配标准电子邮箱地址
	emailRe = regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)
	// bearerRe 匹配 HTTP Authorization Bearer 令牌或 sk- 开头的模型密钥
	bearerRe = regexp.MustCompile(`(?i)(bearer\s+[A-Za-z0-9._\-]{8,}|sk-[A-Za-z0-9._\-]{12,}|ghp_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{20,})`)
	// dsnPassRe 匹配 MySQL DSN 中 user:password@tcp(...) 的口令段
	dsnPassRe = regexp.MustCompile(`([A-Za-z0-9_.\-]+):([^@/\s]+)@tcp\(`)
)

// MaskPII 对用户输入或环境文本中的 IPv4 地址、邮箱及明显凭据做脱敏替换（用于 5.3 节交给大模型前的默认脱敏）。
// 参数 input 为原始文本；返回将 IP 替换为 [IP_REDACTED]、邮箱替换为 [EMAIL_REDACTED]、密钥替换为 [SECRET_REDACTED] 后的安全字符串。
func MaskPII(input string) string {
	if input == "" {
		return ""
	}
	out := emailRe.ReplaceAllString(input, "[EMAIL_REDACTED]")
	out = ipv4Re.ReplaceAllString(out, "[IP_REDACTED]")
	return MaskSecrets(out)
}

// MaskSecrets 仅对日志或会话记录中的密钥、令牌和数据库连接密码进行脱敏（保留代码行号和普通 IP 信息以便排障）。
// 参数 input 为待写入日志或回传诊断的文本；返回脱敏凭据后的安全文本。
func MaskSecrets(input string) string {
	if input == "" {
		return ""
	}
	out := bearerRe.ReplaceAllString(input, "[SECRET_REDACTED]")
	out = dsnPassRe.ReplaceAllString(out, "$1:[PASSWORD_REDACTED]@tcp(")
	return out
}
