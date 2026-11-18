// Package service 实现管理后台初始化、密码+OTP双因素登录、恢复码核销、IP级登录限流以及会话与CSRF令牌管理。
package service

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"autoship/internal/modules/auth/model"
	"autoship/internal/modules/auth/repository"
	"autoship/pkg/crypto"
)

// ErrInvalidCredentials 表示账号密码或动态验证码错误。
var ErrInvalidCredentials = errors.New("用户名、密码或动态验证码错误")

// ErrOTPRequired 表示该管理员已启用 OTP，需要补充提交 6 位动态口令或一次性恢复码。
var ErrOTPRequired = errors.New("需要输入 6 位动态口令")

// ErrAccountLocked 表示连续登录失败触发短期账户保护锁定。
var ErrAccountLocked = errors.New("登录连续失败次数过多，请稍后再试")

// ErrRateLimited 表示来源 IP 请求登录接口过于频繁。
var ErrRateLimited = errors.New("登录请求过于频繁，请稍后再试")

// Service 封装管理后台认证、OTP 绑定与会话生命周期管理。
type Service struct {
	repo       *repository.Repository
	encKey     []byte
	mu         sync.Mutex
	ipLimiters map[string]*rate.Limiter
}

// New 创建管理端认证业务服务。
// 参数 repo 为认证仓储实例，encKey 为 32 字节加密密钥；返回 *Service。
func New(repo *repository.Repository, encKey []byte) *Service {
	return &Service{
		repo:       repo,
		encKey:     encKey,
		ipLimiters: make(map[string]*rate.Limiter),
	}
}

// AllowLoginIP 按客户端 IP 执行令牌桶限流（每秒 1 次，突发最多 5 次）。
// 参数 clientIP 为请求来源 IP；放行返回 true，超速返回 false。
func (s *Service) AllowLoginIP(clientIP string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	lim, ok := s.ipLimiters[clientIP]
	if !ok {
		lim = rate.NewLimiter(rate.Every(time.Second), 5)
		s.ipLimiters[clientIP] = lim
	}
	return lim.Allow()
}

// IsInitialized 判断平台是否已创建至少一名管理员。
// 无入参；返回是否已初始化布尔值及查询错误。
func (s *Service) IsInitialized() (bool, error) {
	cnt, err := s.repo.CountUsers()
	if err != nil {
		return false, err
	}
	return cnt > 0, nil
}

// SetupResult 包含首次初始化创建管理员时一次性展示的 OTP 绑定密钥、二维码 URI、恢复码及登录会话。
type SetupResult struct {
	User          *model.User `json:"user"`          // 管理员信息
	OTPSecret     string      `json:"otpSecret"`     // Base32 密钥
	OTPURI        string      `json:"otpUri"`        // otpauth 二维码链接
	RecoveryCodes []string    `json:"recoveryCodes"` // 一次性明文恢复码
	SessionToken  string      `json:"-"`             // 写入 HttpOnly Cookie 的明文会话令牌
	CSRFToken     string      `json:"csrfToken"`     // 与会话绑定的 CSRF 令牌
}

// InitialSetup 在平台尚未创建任何管理员时初始化首个管理员账号并生成预绑定 OTP 密钥和登录会话。
// 参数 username/password 为管理员初始凭据，clientIP/userAgent 为客户端上下文；成功返回 *SetupResult，若已初始化则拒绝。
func (s *Service) InitialSetup(username, password, clientIP, userAgent string) (*SetupResult, error) {
	initDone, err := s.IsInitialized()
	if err != nil {
		return nil, err
	}
	if initDone {
		return nil, errors.New("系统已完成初始化，禁止重复创建初始管理员")
	}
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return nil, errors.New("用户名和密码不能为空")
	}
	pwdHash, err := crypto.HashPassword(password)
	if err != nil {
		return nil, err
	}
	otpSecret, err := crypto.NewOTPSecret()
	if err != nil {
		return nil, err
	}
	otpSecretEnc, err := crypto.EncryptField(otpSecret, s.encKey)
	if err != nil {
		return nil, err
	}
	codes, hashes, err := crypto.NewRecoveryCodes()
	if err != nil {
		return nil, err
	}
	hashesRaw, _ := json.Marshal(hashes)

	now := time.Now()
	u := &model.User{
		Username:           username,
		PasswordHash:       pwdHash,
		OTPSecretEnc:       otpSecretEnc,
		OTPEnabled:         false,
		RecoveryHashesJSON: string(hashesRaw),
		LastLoginAt:        &now,
		LastLoginIP:        clientIP,
	}
	if err := s.repo.CreateUser(u); err != nil {
		return nil, err
	}

	rawToken, csrfToken, err := s.issueSession(u, clientIP, userAgent)
	if err != nil {
		return nil, err
	}
	return &SetupResult{
		User:          u,
		OTPSecret:     otpSecret,
		OTPURI:        crypto.OTPURI(otpSecret, u.Username),
		RecoveryCodes: codes,
		SessionToken:  rawToken,
		CSRFToken:     csrfToken,
	}, nil
}

// LoginResult 封装登录成功后的会话与 OTP 引导标识。
type LoginResult struct {
	User             *model.User `json:"user"`             // 管理员基本信息
	OTPSetupRequired bool        `json:"otpSetupRequired"` // 若尚未完成 OTP 首次激活则为 true
	SessionToken     string      `json:"-"`                // 写入 HttpOnly Cookie 的明文令牌
	CSRFToken        string      `json:"csrfToken"`        // 会话级 CSRF 令牌
}

// Login 执行 IP 限流、账户锁定检查、bcrypt 口令验证及强制 OTP / 恢复码核验。
// 参数 username/password/otpCode/recoveryCode 为登录凭证，clientIP/userAgent 为请求端信息；成功返回 *LoginResult，失败返回对应错误常量。
func (s *Service) Login(username, password, otpCode, recoveryCode, clientIP, userAgent string) (*LoginResult, error) {
	if !s.AllowLoginIP(clientIP) {
		return nil, ErrRateLimited
	}
	u, err := s.repo.FindByUsername(strings.TrimSpace(username))
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, ErrInvalidCredentials
	}
	now := time.Now()
	if u.LockedUntil != nil && now.Before(*u.LockedUntil) {
		return nil, ErrAccountLocked
	}

	if !crypto.CheckPassword(u.PasswordHash, password) {
		u.FailedLoginCount++
		if u.FailedLoginCount >= 5 {
			until := now.Add(15 * time.Minute)
			u.LockedUntil = &until
		}
		_ = s.repo.UpdateUser(u)
		return nil, ErrInvalidCredentials
	}

	// 若该账号已开启 OTP，必须在行锁事务内校验 6 位动态口令或核销一次性恢复码
	if u.OTPEnabled {
		if strings.TrimSpace(otpCode) == "" && strings.TrimSpace(recoveryCode) == "" {
			return nil, ErrOTPRequired
		}
		verifyErr := s.repo.WithLockedUser(u.ID, func(locked *model.User) error {
			if strings.TrimSpace(otpCode) != "" {
				secret, decErr := crypto.DecryptField(locked.OTPSecretEnc, s.encKey)
				if decErr != nil {
					return ErrInvalidCredentials
				}
				step, ok := crypto.ValidateOTP(secret, strings.TrimSpace(otpCode), now, locked.OTPLastStep)
				if !ok {
					return ErrInvalidCredentials
				}
				locked.OTPLastStep = step
				return nil
			}
			return consumeRecoveryCode(locked, recoveryCode)
		})
		if verifyErr != nil {
			u.FailedLoginCount++
			if u.FailedLoginCount >= 5 {
				until := now.Add(15 * time.Minute)
				u.LockedUntil = &until
			}
			_ = s.repo.UpdateUser(u)
			return nil, ErrInvalidCredentials
		}
	}

	u.FailedLoginCount = 0
	u.LockedUntil = nil
	u.LastLoginAt = &now
	u.LastLoginIP = clientIP
	_ = s.repo.UpdateUser(u)

	rawToken, csrfToken, err := s.issueSession(u, clientIP, userAgent)
	if err != nil {
		return nil, err
	}
	return &LoginResult{
		User:             u,
		OTPSetupRequired: !u.OTPEnabled,
		SessionToken:     rawToken,
		CSRFToken:        csrfToken,
	}, nil
}

// PrepareOTP 为已登录管理员重新生成待绑定的 OTP 密钥与恢复码。
// 参数 userID 为当前管理员 ID；返回 secret、otpauth URI、恢复码列表及错误。
func (s *Service) PrepareOTP(userID uint64) (string, string, []string, error) {
	secret, err := crypto.NewOTPSecret()
	if err != nil {
		return "", "", nil, err
	}
	encSecret, err := crypto.EncryptField(secret, s.encKey)
	if err != nil {
		return "", "", nil, err
	}
	codes, hashes, err := crypto.NewRecoveryCodes()
	if err != nil {
		return "", "", nil, err
	}
	hashesRaw, _ := json.Marshal(hashes)

	var uname string
	err = s.repo.WithLockedUser(userID, func(u *model.User) error {
		uname = u.Username
		u.OTPSecretEnc = encSecret
		u.RecoveryHashesJSON = string(hashesRaw)
		return nil
	})
	if err != nil {
		return "", "", nil, err
	}
	return secret, crypto.OTPURI(secret, uname), codes, nil
}

// EnableOTP 校验管理员输入的首次 6 位动态口令，验证通过后正式激活该账号的 OTP 双因素认证。
// 参数 userID 为管理员 ID，code 为验证器生成的 6 位数字；成功返回 nil，验证码错误返回 ErrInvalidCredentials。
func (s *Service) EnableOTP(userID uint64, code string) error {
	now := time.Now()
	return s.repo.WithLockedUser(userID, func(u *model.User) error {
		secret, err := crypto.DecryptField(u.OTPSecretEnc, s.encKey)
		if err != nil || secret == "" {
			return errors.New("请先生成 OTP 绑定密钥")
		}
		step, ok := crypto.ValidateOTP(secret, strings.TrimSpace(code), now, u.OTPLastStep)
		if !ok {
			return ErrInvalidCredentials
		}
		u.OTPLastStep = step
		u.OTPEnabled = true
		return nil
	})
}

// ChangePassword 校验当前已登录管理员的原密码并更新为新密码（不强制限制密码长度或复杂度，非空即可）。
// 参数 userID 为当前管理员 ID，oldPassword 为原登录密码，newPassword 为新登录密码；成功返回 nil，原密码错误返回 ErrInvalidCredentials，通过 svc.ChangePassword(uid, oldPwd, newPwd) 调用。
func (s *Service) ChangePassword(userID uint64, oldPassword, newPassword string) error {
	if newPassword == "" {
		return errors.New("新密码不能为空")
	}
	pwdHash, err := crypto.HashPassword(newPassword)
	if err != nil {
		return err
	}
	return s.repo.WithLockedUser(userID, func(u *model.User) error {
		if !crypto.CheckPassword(u.PasswordHash, oldPassword) {
			return ErrInvalidCredentials
		}
		u.PasswordHash = pwdHash
		return nil
	})
}

// AuthenticateSession 根据请求携带的明文会话 Cookie 查找有效会话与关联用户。
// 参数 rawSessionToken 为 Cookie 值；有效时返回会话与用户指针，无效返回 (nil, nil, nil)。
func (s *Service) AuthenticateSession(rawSessionToken string) (*model.AdminSession, *model.User, error) {
	if strings.TrimSpace(rawSessionToken) == "" {
		return nil, nil, nil
	}
	hash := hashToken(rawSessionToken)
	sess, err := s.repo.FindActiveSession(hash, time.Now())
	if err != nil || sess == nil {
		return nil, nil, err
	}
	u, err := s.repo.FindByID(sess.UserID)
	if err != nil || u == nil {
		return nil, nil, err
	}
	return sess, u, nil
}

// Logout 销毁指定明文会话令牌在数据库中的会话记录。
// 参数 rawSessionToken 为 Cookie 中的会话令牌；返回删除错误。
func (s *Service) Logout(rawSessionToken string) error {
	if strings.TrimSpace(rawSessionToken) == "" {
		return nil
	}
	return s.repo.DeleteSessionByHash(hashToken(rawSessionToken))
}

// issueSession 生成随机明文会话令牌与绑定的 CSRF 令牌并将令牌哈希持久化到数据库；参数 u 为登录用户，clientIP/userAgent 为请求端信息；返回明文会话令牌、CSRF 令牌及错误。
func (s *Service) issueSession(u *model.User, clientIP, userAgent string) (string, string, error) {
	rawToken, err := crypto.GenerateRandomHex(32)
	if err != nil {
		return "", "", err
	}
	csrfToken, err := crypto.GenerateRandomHex(24)
	if err != nil {
		return "", "", err
	}
	if len(userAgent) > 250 {
		userAgent = userAgent[:250]
	}
	sess := &model.AdminSession{
		SessionToken: hashToken(rawToken),
		CSRFToken:    csrfToken,
		UserID:       u.ID,
		Username:     u.Username,
		ClientIP:     clientIP,
		UserAgent:    userAgent,
		ExpiresAt:    time.Now().Add(24 * time.Hour),
	}
	if err := s.repo.CreateSession(sess); err != nil {
		return "", "", err
	}
	return rawToken, csrfToken, nil
}

// consumeRecoveryCode 在行锁保护下校验并一次性核销用户提交的恢复码；参数 u 为加锁用户实体，inputCode 为输入恢复码；匹配时更新剩余列表并返回 nil，否则返回 ErrInvalidCredentials。
func consumeRecoveryCode(u *model.User, inputCode string) error {
	var hashes []string
	if u.RecoveryHashesJSON != "" {
		_ = json.Unmarshal([]byte(u.RecoveryHashesJSON), &hashes)
	}
	target := crypto.RecoveryCodeHash(inputCode)
	remaining := make([]string, 0, len(hashes))
	matched := false
	for _, h := range hashes {
		if !matched && subtle.ConstantTimeCompare([]byte(h), []byte(target)) == 1 {
			matched = true
			continue
		}
		remaining = append(remaining, h)
	}
	if !matched {
		return ErrInvalidCredentials
	}
	raw, _ := json.Marshal(remaining)
	u.RecoveryHashesJSON = string(raw)
	return nil
}

// hashToken 计算明文会话令牌的带域分离 SHA-256 十六进制摘要；参数 raw 为 Cookie 明文值；返回 64 字符 hex 摘要。
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte("autoship-session-v1:" + raw))
	return hex.EncodeToString(sum[:])
}
