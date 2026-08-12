package admin

import (
	"context"
	"sync"
	"sync/atomic"

	"stressbot/controlplane/controlv1"
)

const sessionQueueCapacity = 64

type agentSession struct {
	agentID    string
	generation uint64
	ctx        context.Context
	cancel     context.CancelFunc
	commands   chan struct{}
	control    chan *controlv1.AdminEvent
	heartbeat  chan *controlv1.HeartbeatAck
	lastSent   atomic.Uint64
	commandMu  sync.Mutex
	inflight   map[string]struct{}
	orphanStop atomic.Bool
}

func newAgentSession(parent context.Context, agentID string, generation uint64) *agentSession {
	ctx, cancel := context.WithCancel(parent)
	session := &agentSession{
		agentID: agentID, generation: generation, ctx: ctx, cancel: cancel,
		commands:  make(chan struct{}, 1),
		control:   make(chan *controlv1.AdminEvent, sessionQueueCapacity),
		heartbeat: make(chan *controlv1.HeartbeatAck, 1),
		inflight:  make(map[string]struct{}),
	}
	return session
}

func (s *agentSession) commandCapacity() int {
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	return commandDispatchWindow - len(s.inflight)
}

func (s *agentSession) markCommandSent(commandID string) {
	s.commandMu.Lock()
	s.inflight[commandID] = struct{}{}
	s.commandMu.Unlock()
}

func (s *agentSession) markCommandAcknowledged(commandID string) {
	s.commandMu.Lock()
	delete(s.inflight, commandID)
	s.commandMu.Unlock()
	s.wakeCommands()
}

func (s *agentSession) wakeCommands() {
	select {
	case s.commands <- struct{}{}:
	default:
	}
}

func (s *agentSession) offerControl(event *controlv1.AdminEvent) bool {
	select {
	case s.control <- event:
		return true
	default:
		return false
	}
}

func (s *agentSession) offerHeartbeat(ack *controlv1.HeartbeatAck) {
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

type SessionRegistry struct {
	mu         sync.RWMutex
	sessions   map[string]*agentSession
	generation map[string]uint64
	gates      map[string]*sync.Mutex
	closing    bool
}

func NewSessionRegistry() *SessionRegistry {
	return &SessionRegistry{sessions: make(map[string]*agentSession), generation: make(map[string]uint64), gates: make(map[string]*sync.Mutex)}
}

func (r *SessionRegistry) Attach(parent context.Context, agentID string) (*agentSession, error) {
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
	session := newAgentSession(parent, agentID, r.generation[agentID])
	old := r.sessions[agentID]
	r.sessions[agentID] = session
	if old != nil {
		old.cancel()
	}
	return session, nil
}

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

func (r *SessionRegistry) Detach(agentID string, generation uint64) {
	r.mu.Lock()
	if session := r.sessions[agentID]; session != nil && session.generation == generation {
		delete(r.sessions, agentID)
	}
	r.mu.Unlock()
}

func (r *SessionRegistry) Current(agentID string, generation uint64) bool {
	r.mu.RLock()
	session := r.sessions[agentID]
	current := session != nil && session.generation == generation
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
		session.wakeCommands()
	}
	r.mu.RUnlock()
}

func (r *SessionRegistry) Close(reason string) {
	r.mu.Lock()
	r.closing = true
	sessions := make([]*agentSession, 0, len(r.sessions))
	for _, session := range r.sessions {
		sessions = append(sessions, session)
	}
	r.mu.Unlock()
	for _, session := range sessions {
		session.offerControl(&controlv1.AdminEvent{Event: &controlv1.AdminEvent_ServerClosing{ServerClosing: &controlv1.ServerClosing{Reason: reason}}})
		session.cancel()
	}
}
