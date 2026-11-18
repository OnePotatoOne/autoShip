// Package envelope 定义 autoShip 各端通信的统一消息信封（7.1 节）及面向 autoship-apply 的服务端签名动作票据协议。
package envelope

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"autoship/pkg/crypto"
)

// Message 是 Server 与开发 Agent / 更新 Agent 之间传输指令和事件的统一消息信封。
// 接收方按 ID 做幂等去重；事件流携带单调递增 Seq，断线重连后基于 Ack 续传。
type Message struct {
	ID      string          `json:"id"`                // 全局唯一消息/指令 ID，用于至少一次投递下的接收端幂等去重
	Type    string          `json:"type"`              // 消息类型，例如 cmd.work.start、cmd.release.publish、cmd.deploy.execute、evt.heartbeat、evt.ack
	Seq     uint64          `json:"seq"`               // 发送端单调递增序号，用于断线续传
	Ack     uint64          `json:"ack"`               // 对端已确认处理的最大序号
	TS      int64           `json:"ts"`                // 发送时刻的秒级 Unix 时间戳
	Payload json.RawMessage `json:"payload,omitempty"` // 具体指令或事件的 JSON 载荷
}

// NewMessage 创建一条填充当前时间戳与 JSON 序列化载荷的统一信封消息。
// 参数 id 为唯一标识，msgType 为消息类型，seq/ack 为序号与确认号，payload 为任意可 JSON 序列化的结构体（可为 nil）；返回 Message 实例及序列化错误。
func NewMessage(id, msgType string, seq, ack uint64, payload any) (Message, error) {
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return Message{}, fmt.Errorf("序列化信封载荷失败: %w", err)
		}
		raw = b
	}
	return Message{
		ID:      id,
		Type:    msgType,
		Seq:     seq,
		Ack:     ack,
		TS:      time.Now().Unix(),
		Payload: raw,
	}, nil
}

// safeIdentifierRe 约束产品标识、部署 ID 与目标 ID 仅能由安全 ASCII 字符组成，杜绝路径穿越与命令注入。
var safeIdentifierRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,64}$`)

// ValidateSafeID 校验标识符（如 deployment_id、product_code）是否符合安全字符集规则。
// 参数 id 为待校验字符串；合法返回 nil，非法返回错误。
func ValidateSafeID(id string) error {
	if !safeIdentifierRe.MatchString(id) {
		return fmt.Errorf("标识符格式非法: %q", id)
	}
	return nil
}

// ActionTicket 是 Server 在后台确认部署、手动回滚或手动备份后签发的受保护动作票据。
// autoship-apply（root）在无通配 sudo 启动后，从固定暂存目录复制该票据并用 root 持有的 Server 公钥验签，从而安全提取 deployment_id，杜绝被攻破的常驻进程伪造回滚或升级参数。
type ActionTicket struct {
	TicketID      string `json:"ticketId"`      // 票据唯一 ID
	Action        string `json:"action"`        // 允许动作：upgrade | rollback | backup
	ProductCode   string `json:"productCode"`   // 产品唯一编码
	TargetID      uint64 `json:"targetId"`      // 部署目标 ID
	DeploymentID  string `json:"deploymentId"`  // 部署记录唯一标识（传递给产品部署钩子 --deployment）
	ReleaseID     uint64 `json:"releaseId"`     // 关联版本 ID
	Version       string `json:"version"`       // 目标版本号（如 v1.2.0）
	BackupDB      bool   `json:"backupDb"`      // 本次升级前是否必须先执行数据库备份
	IssuedAt      int64  `json:"issuedAt"`      // 签发秒级时间戳
	ExpiresAt     int64  `json:"expiresAt"`     // 过期秒级时间戳
	ServerSigB64  string `json:"serverSigB64"`  // Server Ed25519 私钥对规范化字段的分离签名
}

// canonicalPayload 构造 ActionTicket 除签名字段外的确定性签名原文。
// 无入参；返回用于签名或验签的字节切片。
func (t ActionTicket) canonicalPayload() []byte {
	raw := fmt.Sprintf("v1|%s|%s|%s|%d|%s|%d|%s|%t|%d|%d",
		t.TicketID, t.Action, t.ProductCode, t.TargetID,
		t.DeploymentID, t.ReleaseID, t.Version, t.BackupDB,
		t.IssuedAt, t.ExpiresAt,
	)
	return []byte(raw)
}

// Sign 使用 Server 的 Ed25519 私钥为动作票据生成签名并写入 ServerSigB64 字段。
// 参数 serverPrivBase64 为服务端签名私钥；成功返回 nil，失败返回错误。
func (t *ActionTicket) Sign(serverPrivBase64 string) error {
	if err := ValidateSafeID(t.ProductCode); err != nil {
		return fmt.Errorf("票据产品编码非法: %w", err)
	}
	if err := ValidateSafeID(t.DeploymentID); err != nil {
		return fmt.Errorf("票据部署 ID 非法: %w", err)
	}
	sig, err := crypto.SignBytes(serverPrivBase64, t.canonicalPayload())
	if err != nil {
		return err
	}
	t.ServerSigB64 = sig
	return nil
}

// Verify 使用 root 持有的 Server Ed25519 公钥校验动作票据签名、动作枚举、有效期及标识符安全性。
// 参数 serverPubBase64 为服务端验证公钥，expectedAction 为当前 sudo 子命令对应的动作（upgrade/rollback/backup），now 为当前时间；校验通过返回 nil，否则返回错误。
func (t ActionTicket) Verify(serverPubBase64, expectedAction string, now time.Time) error {
	if t.Action != expectedAction {
		return fmt.Errorf("票据动作不匹配: 期望 %s，实际 %s", expectedAction, t.Action)
	}
	if err := ValidateSafeID(t.ProductCode); err != nil {
		return fmt.Errorf("票据产品编码非法: %w", err)
	}
	if err := ValidateSafeID(t.DeploymentID); err != nil {
		return fmt.Errorf("票据部署 ID 非法: %w", err)
	}
	nowUnix := now.Unix()
	if nowUnix > t.ExpiresAt || t.IssuedAt-300 > nowUnix {
		return errors.New("动作票据已过期或签发时间异常")
	}
	return crypto.VerifyBytes(serverPubBase64, t.canonicalPayload(), t.ServerSigB64)
}
