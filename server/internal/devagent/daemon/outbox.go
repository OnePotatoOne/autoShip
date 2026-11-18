// Package daemon 实现 Mac 开发 Agent 的常驻 WSS 守护连接（`/agent/v1/dev/ws`）、15 秒心跳、断线指数退避重连、离线 JSONL 发件箱断点续传、指令幂等去重分发以及可选的本地独立 MySQL `autoship_agent` 状态持久化（设计文档 7.1 ~ 7.3 & 8.1 节）。
package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"

	"autoship/pkg/envelope"
)

// Outbox 维护本地落盘的递增序号事件发件箱（JSONL 格式），支持离线缓存与重连后按 Server 最后确认的 Ack 序号断点续传（7.1 & 7.2 节）。
type Outbox struct {
	mu      sync.Mutex
	path    string
	nextSeq uint64
	pending []envelope.Message
}

// OpenOutbox 打开或创建指定路径的 JSONL 发件箱文件，恢复未确认的挂起事件及单调递增序号。
// 参数 jsonlPath 为发件箱磁盘路径；返回 *Outbox 及读写错误。
func OpenOutbox(jsonlPath string) (*Outbox, error) {
	if err := os.MkdirAll(filepath.Dir(jsonlPath), 0755); err != nil {
		return nil, fmt.Errorf("创建发件箱目录失败: %w", err)
	}
	ob := &Outbox{
		path:    jsonlPath,
		nextSeq: 1,
	}
	f, err := os.Open(jsonlPath)
	if os.IsNotExist(err) {
		return ob, nil
	}
	if err != nil {
		return nil, fmt.Errorf("打开本地发件箱失败: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// 放宽单行上限至 4MB 以容纳完整验收包事件载荷
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg envelope.Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		ob.pending = append(ob.pending, msg)
		if msg.Seq >= ob.nextSeq {
			ob.nextSeq = msg.Seq + 1
		}
	}
	return ob, scanner.Err()
}

// Enqueue 为新事件分配下一个单调递增的 Seq 并立即追加写入 JSONL 发件箱文件，确保断线或进程崩溃不丢事件。
// 参数 msgType 为事件类型（如 evt.work.ready_for_acceptance），ackToServer 为当前已确认的最大对端指令序号，payload 为事件载荷；返回封装好的 envelope.Message 及持久化错误。
func (o *Outbox) Enqueue(msgType string, ackToServer uint64, payload any) (envelope.Message, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	seq := o.nextSeq
	msg, err := envelope.NewMessage(uuid.NewString(), msgType, seq, ackToServer, payload)
	if err != nil {
		return envelope.Message{}, err
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return envelope.Message{}, err
	}

	f, err := os.OpenFile(o.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return envelope.Message{}, fmt.Errorf("写入发件箱文件失败: %w", err)
	}
	_, writeErr := f.Write(append(raw, '\n'))
	closeErr := f.Close()
	if writeErr != nil {
		return envelope.Message{}, writeErr
	}
	if closeErr != nil {
		return envelope.Message{}, closeErr
	}

	o.nextSeq++
	o.pending = append(o.pending, msg)
	return msg, nil
}

// AckUpTo 在收到 Server 返回的确认号 ackSeq 后剔除所有 `Seq <= ackSeq` 的已确认事件并压缩重写 JSONL 文件。
// 参数 ackSeq 为 Server 已确认的最大事件序号；返回重写错误。
func (o *Outbox) AckUpTo(ackSeq uint64) error {
	if ackSeq == 0 {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	var remaining []envelope.Message
	changed := false
	for _, m := range o.pending {
		if m.Seq <= ackSeq {
			changed = true
			continue
		}
		remaining = append(remaining, m)
	}
	if !changed {
		return nil
	}
	o.pending = remaining
	return o.rewriteLocked()
}

// PendingAfter 返回序号严格大于 afterSeq 的全部待续传事件副本（按 Seq 升序排列）。
// 参数 afterSeq 为对端最后确认的序号（传 0 返回全部未确认事件）；返回 []envelope.Message。
func (o *Outbox) PendingAfter(afterSeq uint64) []envelope.Message {
	o.mu.Lock()
	defer o.mu.Unlock()

	var out []envelope.Message
	for _, m := range o.pending {
		if m.Seq > afterSeq {
			out = append(out, m)
		}
	}
	return out
}

// PendingCount 返回当前尚未被 Server 确认的本地挂起事件条数。
// 无入参；返回挂起条数。
func (o *Outbox) PendingCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.pending)
}

// rewriteLocked 在持有互斥锁的前提下原子重写压缩后的发件箱文件。
func (o *Outbox) rewriteLocked() error {
	tmpPath := o.path + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	for _, m := range o.pending {
		raw, err := json.Marshal(m)
		if err != nil {
			_ = f.Close()
			return err
		}
		if _, err := f.Write(append(raw, '\n')); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, o.path)
}
