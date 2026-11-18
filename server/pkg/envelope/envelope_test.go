// Package envelope_test 验证统一消息信封序列化与无通配 sudo 动作票据的 Ed25519 签名防伪能力。
package envelope_test

import (
	"testing"
	"time"

	"autoship/pkg/crypto"
	"autoship/pkg/envelope"
)

// TestActionTicketSignAndVerify 验证合法票据验签通过，篡改 deployment_id 或动作类型立即被拒绝。
func TestActionTicketSignAndVerify(t *testing.T) {
	pub, priv, err := crypto.GenerateEd25519KeyPair()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tkt := envelope.ActionTicket{
		TicketID:     "tkt_1001",
		Action:       "rollback",
		ProductCode:  "seccenter",
		TargetID:     3,
		DeploymentID: "dep_42_abc",
		ReleaseID:    9,
		Version:      "v1.4.0",
		IssuedAt:     now.Unix(),
		ExpiresAt:    now.Add(time.Hour).Unix(),
	}
	if err := tkt.Sign(priv); err != nil {
		t.Fatalf("票据签名失败: %v", err)
	}
	if err := tkt.Verify(pub, "rollback", now); err != nil {
		t.Fatalf("合法票据验签失败: %v", err)
	}
	if err := tkt.Verify(pub, "upgrade", now); err == nil {
		t.Fatalf("动作类型不匹配未被拦截")
	}
	tampered := tkt
	tampered.DeploymentID = "dep_1_old"
	if err := tampered.Verify(pub, "rollback", now); err == nil {
		t.Fatalf("篡改 deployment_id 未被验签拦截")
	}
}
