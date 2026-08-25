// Package session 实现 Agent 与 Admin 的 gRPC 控制会话：会话生命周期监督与重连、
// 事件收发循环、心跳生产，以及命令结果与任务最终报告的待确认留存与重放。
package session

import (
	"errors"
	"fmt"
	"sync"

	controlpb "stressbot/controlplane/pb"

	"google.golang.org/protobuf/proto"
)

type pendingReport struct {
	report *controlpb.FinalReport
	done   chan error
}

// ReportOutbox 留存任务最终报告直到 Admin 确认（容量有界），会话重建后可重放；
// 每份报告关联独立的完成通道，供提交方等待确认结果。
type ReportOutbox struct {
	mu       sync.Mutex
	capacity int
	items    map[string]*pendingReport
	order    []string
	notify   chan struct{}
}

// NewReportOutbox 创建容量有界的最终报告待确认队列；capacity <= 0 时取默认 128。
func NewReportOutbox(capacity int) *ReportOutbox {
	if capacity <= 0 {
		capacity = 128
	}
	return &ReportOutbox{capacity: capacity, items: make(map[string]*pendingReport), notify: make(chan struct{}, 1)}
}

// Offer 克隆并存入一份最终报告并唤醒发送循环，返回该报告的确认结果通道；
// 同一 ReportId 重复提交直接返回既有通道，容量已满时返回错误。
func (o *ReportOutbox) Offer(report *controlpb.FinalReport) (<-chan error, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if existing := o.items[report.ReportId]; existing != nil {
		return existing.done, nil
	}
	if len(o.items) >= o.capacity {
		return nil, errors.New("最终报告待确认队列已满")
	}
	pending := &pendingReport{report: proto.Clone(report).(*controlpb.FinalReport), done: make(chan error, 1)}
	o.items[report.ReportId] = pending
	o.order = append(o.order, report.ReportId)
	o.wakeLocked()
	return pending.done, nil
}

// Snapshot 按提交顺序返回所有未确认报告的克隆，供会话（重）建立时批量重放；
// 不改变各报告的留存状态。
func (o *ReportOutbox) Snapshot() []*controlpb.FinalReport {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]*controlpb.FinalReport, 0, len(o.order))
	for _, id := range o.order {
		if pending := o.items[id]; pending != nil {
			out = append(out, proto.Clone(pending.report).(*controlpb.FinalReport))
		}
	}
	return out
}

// Acknowledge 处理 Admin 的报告确认：移除对应报告，并按 Accepted 向等待方
// 传递 nil（接收）或拒绝原因；未知 ReportId 静默忽略。
func (o *ReportOutbox) Acknowledge(ack *controlpb.ReportAck) {
	o.mu.Lock()
	pending := o.items[ack.ReportId]
	if pending != nil {
		delete(o.items, ack.ReportId)
		for i, id := range o.order {
			if id == ack.ReportId {
				o.order = append(o.order[:i], o.order[i+1:]...)
				break
			}
		}
	}
	o.mu.Unlock()
	if pending == nil {
		return
	}
	if ack.Accepted {
		pending.done <- nil
	} else {
		pending.done <- fmt.Errorf("admin 拒绝最终报告: %s", ack.Reason)
	}
	close(pending.done)
}

// Wake 手动向通知通道发送一次边沿信号，唤醒等待报告发送的循环。
func (o *ReportOutbox) Wake() { o.mu.Lock(); o.wakeLocked(); o.mu.Unlock() }

// Notifications 返回报告队列边沿通知通道。
func (o *ReportOutbox) Notifications() <-chan struct{} { return o.notify }

func (o *ReportOutbox) wakeLocked() {
	select {
	case o.notify <- struct{}{}:
	default:
	}
}
