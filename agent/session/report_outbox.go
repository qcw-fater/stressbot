package session

import (
	"fmt"
	"sync"

	"stressbot/controlplane/pb"

	"google.golang.org/protobuf/proto"
)

type pendingReport struct {
	report *controlpb.FinalReport
	done   chan error
}

type ReportOutbox struct {
	mu       sync.Mutex
	capacity int
	items    map[string]*pendingReport
	order    []string
	notify   chan struct{}
}

func NewReportOutbox(capacity int) *ReportOutbox {
	if capacity <= 0 {
		capacity = 128
	}
	return &ReportOutbox{capacity: capacity, items: make(map[string]*pendingReport), notify: make(chan struct{}, 1)}
}

func (o *ReportOutbox) Offer(report *controlpb.FinalReport) (<-chan error, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if existing := o.items[report.ReportId]; existing != nil {
		return existing.done, nil
	}
	if len(o.items) >= o.capacity {
		return nil, fmt.Errorf("最终报告待确认队列已满")
	}
	pending := &pendingReport{report: proto.Clone(report).(*controlpb.FinalReport), done: make(chan error, 1)}
	o.items[report.ReportId] = pending
	o.order = append(o.order, report.ReportId)
	o.wakeLocked()
	return pending.done, nil
}

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

func (o *ReportOutbox) Wake() { o.mu.Lock(); o.wakeLocked(); o.mu.Unlock() }

// Notifications 返回报告队列边沿通知通道。
func (o *ReportOutbox) Notifications() <-chan struct{} { return o.notify }

func (o *ReportOutbox) wakeLocked() {
	select {
	case o.notify <- struct{}{}:
	default:
	}
}
