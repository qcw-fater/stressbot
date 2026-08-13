package grpcapi

import (
	"context"
	"sync"
	"sync/atomic"

	"stressbot/controlplane/pb"
)

const sessionQueueCapacity = 64

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

func (s *Session) CommandCapacity() int {
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	return CommandDispatchWindow - len(s.inflight)
}

func (s *Session) MarkCommandSent(commandID string) {
	s.commandMu.Lock()
	s.inflight[commandID] = struct{}{}
	s.commandMu.Unlock()
}

func (s *Session) MarkCommandAcknowledged(commandID string) {
	s.commandMu.Lock()
	delete(s.inflight, commandID)
	s.commandMu.Unlock()
	s.WakeCommands()
}

func (s *Session) WakeCommands() {
	select {
	case s.commands <- struct{}{}:
	default:
	}
}

func (s *Session) OfferControl(event *controlpb.AdminEvent) bool {
	select {
	case s.control <- event:
		return true
	default:
		return false
	}
}

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

func (s *Session) AgentID() string                           { return s.agentID }
func (s *Session) Generation() uint64                        { return s.generation }
func (s *Session) Context() context.Context                  { return s.ctx }
func (s *Session) Cancel()                                   { s.cancel() }
func (s *Session) Commands() <-chan struct{}                 { return s.commands }
func (s *Session) Control() <-chan *controlpb.AdminEvent     { return s.control }
func (s *Session) Heartbeat() <-chan *controlpb.HeartbeatAck { return s.heartbeat }
func (s *Session) LastSent() *atomic.Uint64                  { return &s.lastSent }
func (s *Session) TryMarkOrphanStop() bool                   { return s.orphanStop.CompareAndSwap(false, true) }
func (s *Session) ClearOrphanStop()                          { s.orphanStop.Store(false) }

const CommandDispatchWindow = 16

type SessionRegistry struct {
	mu         sync.RWMutex
	sessions   map[string]*Session
	generation map[string]uint64
	gates      map[string]*sync.Mutex
	closing    bool
}

func NewSessionRegistry() *SessionRegistry {
	return &SessionRegistry{sessions: make(map[string]*Session), generation: make(map[string]uint64), gates: make(map[string]*sync.Mutex)}
}

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
	if session := r.sessions[agentID]; session != nil && session.Generation() == generation {
		delete(r.sessions, agentID)
	}
	r.mu.Unlock()
}

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
