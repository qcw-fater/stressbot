package grpcapi

import (
	"context"
	"sync"
	"sync/atomic"

	controlpb "stressbot/controlplane/pb"
)

const sessionQueueCapacity = 64

// Session 是单个 Agent 连接的会话状态：commands/control/heartbeat 三个
// 事件通道驱动发送循环，inflight 记录在途命令以实施投递窗口控制，
// lastSent 是投递游标（非 ACK 游标），orphanStop 防止孤儿任务停止命令
// 重复下发。
type Session struct {
	agentID    string
	generation uint64
	ctx        context.Context
	cancel     context.CancelFunc
	commands   chan struct{}
	control    chan *controlpb.AdminEvent
	heartbeat  chan *controlpb.HeartbeatAck
	lastSent   atomic.Uint64
	commandMu  sync.Mutex
	inflight   map[string]struct{}
	orphanStop atomic.Bool
}

func newSession(parent context.Context, agentID string, generation uint64) *Session {
	ctx, cancel := context.WithCancel(parent)
	session := &Session{
		agentID: agentID, generation: generation, ctx: ctx, cancel: cancel,
		commands:  make(chan struct{}, 1),
		control:   make(chan *controlpb.AdminEvent, sessionQueueCapacity),
		heartbeat: make(chan *controlpb.HeartbeatAck, 1),
		inflight:  make(map[string]struct{}),
	}
	return session
}

// CommandCapacity 返回会话当前可继续投递的命令配额：投递窗口减去在途
// 未确认数量。
func (s *Session) CommandCapacity() int {
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	return CommandDispatchWindow - len(s.inflight)
}

// MarkCommandSent 把命令计入在途集合，占用一个投递窗口配额。
func (s *Session) MarkCommandSent(commandID string) {
	s.commandMu.Lock()
	s.inflight[commandID] = struct{}{}
	s.commandMu.Unlock()
}

// MarkCommandAcknowledged 将命令移出在途集合并唤醒发送循环补投下一批。
func (s *Session) MarkCommandAcknowledged(commandID string) {
	s.commandMu.Lock()
	delete(s.inflight, commandID)
	s.commandMu.Unlock()
	s.WakeCommands()
}

// WakeCommands 发出非阻塞的命令就绪信号，驱动发送循环拉取未决命令；
// 信号已在途时合并为一次。
func (s *Session) WakeCommands() {
	select {
	case s.commands <- struct{}{}:
	default:
	}
}

// OfferControl 向控制事件队列非阻塞投递（命令、回执、服务器关闭事件）；
// 队列满时返回 false，由调用方决定报错。
func (s *Session) OfferControl(event *controlpb.AdminEvent) bool {
	select {
	case s.control <- event:
		return true
	default:
		return false
	}
}

// OfferHeartbeat 投递心跳回执；通道被占用时丢弃旧回执、只保留最新一条。
func (s *Session) OfferHeartbeat(ack *controlpb.HeartbeatAck) {
	select {
	case s.heartbeat <- ack:
	default:
		select {
		case <-s.heartbeat:
		default:
		}
		select {
		case s.heartbeat <- ack:
		default:
		}
	}
}

// AgentID 返回会话所属的 Agent 标识。
func (s *Session) AgentID() string { return s.agentID }

// Generation 返回会话代号；同一 Agent 每次重连递增，用于识别当前会话。
func (s *Session) Generation() uint64 { return s.generation }

// Context 返回随会话取消的 context（派生自会话流 context）。
func (s *Session) Context() context.Context { return s.ctx }

// Cancel 取消会话 context，终止其收发循环。
func (s *Session) Cancel() { s.cancel() }

// Commands 返回命令就绪信号通道（容量 1 的边沿信号）。
func (s *Session) Commands() <-chan struct{} { return s.commands }

// Control 返回控制事件下发队列。
func (s *Session) Control() <-chan *controlpb.AdminEvent { return s.control }

// Heartbeat 返回心跳回执通道（容量 1，只保留最新）。
func (s *Session) Heartbeat() <-chan *controlpb.HeartbeatAck { return s.heartbeat }

// LastSent 返回投递游标：该会话最近已下发命令的 Sequence。
func (s *Session) LastSent() *atomic.Uint64 { return &s.lastSent }

// TryMarkOrphanStop 以 CAS 置位孤儿停止标记，仅首次置位返回 true，
// 用于避免对同一孤儿任务重复下发停止命令。
func (s *Session) TryMarkOrphanStop() bool { return s.orphanStop.CompareAndSwap(false, true) }

// ClearOrphanStop 复位孤儿停止标记，停止命令失败后允许重试。
func (s *Session) ClearOrphanStop() { s.orphanStop.Store(false) }

// CommandDispatchWindow 是单会话允许的在途（已下发未 ACK）命令数上限。
const CommandDispatchWindow = 16

// SessionRegistry 按 Agent 管理当前会话：Attach 递增 generation 并顶替旧
// 会话；per-Agent gate 串行化 Attach 与 WithCurrent，避免会话被顶替后旧
// 流仍继续写入注册表。
type SessionRegistry struct {
	mu         sync.RWMutex
	sessions   map[string]*Session
	generation map[string]uint64
	gates      map[string]*sync.Mutex
	closing    bool
}

// NewSessionRegistry 创建空的会话注册表。
func NewSessionRegistry() *SessionRegistry {
	return &SessionRegistry{sessions: make(map[string]*Session), generation: make(map[string]uint64), gates: make(map[string]*sync.Mutex)}
}

// Attach 为 Agent 建立新一代会话并顶替旧会话（取消其 context）；在
// per-Agent gate 下串行执行；注册表已关闭时返回 context.Canceled。
func (r *SessionRegistry) Attach(parent context.Context, agentID string) (*Session, error) {
	r.mu.Lock()
	gate := r.gates[agentID]
	if gate == nil {
		gate = new(sync.Mutex)
		r.gates[agentID] = gate
	}
	r.mu.Unlock()
	gate.Lock()
	defer gate.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return nil, context.Canceled
	}
	r.generation[agentID]++
	session := newSession(parent, agentID, r.generation[agentID])
	old := r.sessions[agentID]
	r.sessions[agentID] = session
	if old != nil {
		old.Cancel()
	}
	return session, nil
}

// WithCurrent 在持有该 Agent gate 且指定代仍是当前会话时执行 fn，否则
// 返回 context.Canceled，用于旧会话事件落库前的当前性检查。
func (r *SessionRegistry) WithCurrent(agentID string, generation uint64, fn func() error) error {
	r.mu.RLock()
	gate := r.gates[agentID]
	r.mu.RUnlock()
	if gate == nil {
		return context.Canceled
	}
	gate.Lock()
	defer gate.Unlock()
	if !r.Current(agentID, generation) {
		return context.Canceled
	}
	return fn()
}

// Detach 注销会话，仅当其代数与当前代一致时生效。
func (r *SessionRegistry) Detach(agentID string, generation uint64) {
	r.mu.Lock()
	if session := r.sessions[agentID]; session != nil && session.Generation() == generation {
		delete(r.sessions, agentID)
	}
	r.mu.Unlock()
}

// Current 报告指定代的会话是否仍是该 Agent 的当前会话。
func (r *SessionRegistry) Current(agentID string, generation uint64) bool {
	r.mu.RLock()
	session := r.sessions[agentID]
	current := session != nil && session.Generation() == generation
	r.mu.RUnlock()
	return current
}

// WakeCommands selects and signals the current session under r.mu. Attach
// cannot replace that pointer between selection and the non-blocking wake;
// commands durable before Attach are found by Replay, while later commands
// necessarily wake the replacement session.
func (r *SessionRegistry) WakeCommands(agentID string) {
	r.mu.RLock()
	session := r.sessions[agentID]
	if session != nil {
		session.WakeCommands()
	}
	r.mu.RUnlock()
}

// Close 置注册表为关闭态：向全部会话广播 ServerClosing 事件并取消其
// context；此后 Attach 一律返回 context.Canceled。
func (r *SessionRegistry) Close(reason string) {
	r.mu.Lock()
	r.closing = true
	sessions := make([]*Session, 0, len(r.sessions))
	for _, session := range r.sessions {
		sessions = append(sessions, session)
	}
	r.mu.Unlock()
	for _, session := range sessions {
		session.OfferControl(&controlpb.AdminEvent{Event: &controlpb.AdminEvent_ServerClosing{ServerClosing: &controlpb.ServerClosing{Reason: reason}}})
		session.Cancel()
	}
}
