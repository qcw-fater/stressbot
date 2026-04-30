package admin

import (
	"fmt"
	"sync"
	"time"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// UpgradeOrchestrator 升级编排器。
type UpgradeOrchestrator struct {
	agents     *AgentRegistry
	binaries   *BinaryStore
	dispatcher *AgentDispatcher
	publicURL  string
	rolloutDelay    time.Duration
	perAgentTimeout time.Duration

	mu      sync.Mutex
	current *upgradeJob
}

type upgradeJob struct {
	version  string
	targets  []string
	perAgent map[string]*AgentUpgradeState
	cancelCh chan struct{}
}

func NewUpgradeOrchestrator(
	agents *AgentRegistry,
	binaries *BinaryStore,
	dispatcher *AgentDispatcher,
	publicURL string,
	rolloutDelay time.Duration,
	perAgentTimeout time.Duration,
) *UpgradeOrchestrator {
	return &UpgradeOrchestrator{
		agents:          agents,
		binaries:        binaries,
		dispatcher:      dispatcher,
		publicURL:       publicURL,
		rolloutDelay:    rolloutDelay,
		perAgentTimeout: perAgentTimeout,
	}
}

// UpgradeOne 单点升级。
func (o *UpgradeOrchestrator) UpgradeOne(agentID, version string) error {
	o.mu.Lock()
	if o.current != nil {
		o.mu.Unlock()
		return ErrUpgradeInProgress
	}
	o.mu.Unlock()

	return o.doUpgrade(agentID, version)
}

// UpgradeAll 滚动升级所有在线 Agent。
func (o *UpgradeOrchestrator) UpgradeAll(version string) error {
	o.mu.Lock()
	if o.current != nil {
		o.mu.Unlock()
		return ErrUpgradeInProgress
	}

	agents := o.agents.List()
	var targets []string
	for _, a := range agents {
		if a.Status != AgentOffline {
			targets = append(targets, a.ID)
		}
	}
	o.mu.Unlock()

	if len(targets) == 0 {
		return ErrCapacityExceeded.WithMessage("no online agents to upgrade")
	}

	// 初始化 job
	job := &upgradeJob{
		version:  version,
		targets:  targets,
		perAgent: make(map[string]*AgentUpgradeState),
		cancelCh: make(chan struct{}),
	}

	o.mu.Lock()
	o.current = job
	o.mu.Unlock()

	// 异步滚动
	go o.rollout(job)
	return nil
}

// Status 查询升级状态。
func (o *UpgradeOrchestrator) Status() *UpgradeStatus {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.current == nil {
		return &UpgradeStatus{PerAgent: make(map[string]AgentUpgradeState)}
	}

	job := o.current
	status := &UpgradeStatus{
		InProgress: true,
		Version:    job.version,
		Total:      len(job.targets),
		PerAgent:   make(map[string]AgentUpgradeState),
	}

	for id, state := range job.perAgent {
		status.PerAgent[id] = *state
		switch state.Phase {
		case "success":
			status.Completed++
		case "failed":
			status.Failed++
		case "sent", "upgrading":
			status.CurrentAgentID = id
		}
	}
	return status
}

// Cancel 取消滚动升级（已发出的不撤回）。
func (o *UpgradeOrchestrator) Cancel() error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.current == nil {
		return nil
	}
	close(o.current.cancelCh)
	o.current = nil
	return nil
}

func (o *UpgradeOrchestrator) rollout(job *upgradeJob) {
	defer func() {
		o.mu.Lock()
		o.current = nil
		o.mu.Unlock()
	}()

	binaryURL := fmt.Sprintf("%s/api/binaries/agent-%s.exe", o.publicURL, job.version)
	meta, ok := o.binaries.Get("agent-" + job.version + ".exe")
	if !ok {
		stresslog.Error("升级二进制不存在", zap.String("version", job.version))
		return
	}

	for _, agentID := range job.targets {
		select {
		case <-job.cancelCh:
			return
		default:
		}

		agent, ok := o.agents.Get(agentID)
		if !ok || agent.Status == AgentOffline {
			job.perAgent[agentID] = &AgentUpgradeState{Phase: "failed", Error: "agent offline"}
			continue
		}

		job.perAgent[agentID] = &AgentUpgradeState{
			Phase:     "sent",
			StartedAt: time.Now(),
		}

		err := o.dispatcher.Upgrade(agent.Address, UpgradeRequest{
			URL:     binaryURL,
			SHA256:  meta.SHA256,
			Version: job.version,
		})
		if err != nil {
			job.perAgent[agentID].Phase = "failed"
			job.perAgent[agentID].Error = err.Error()
			stresslog.Error("升级命令发送失败",
				zap.String("agentId", agentID),
				zap.Error(err))
			continue
		}

		job.perAgent[agentID].Phase = "upgrading"
		stresslog.Info("升级命令已发送",
			zap.String("agentId", agentID),
			zap.String("version", job.version))

		// 等待 Agent 重新注册（版本号变化）
		if err := o.waitForVersion(agentID, job.version, job); err != nil {
			job.perAgent[agentID].Phase = "failed"
			job.perAgent[agentID].Error = err.Error()
			stresslog.Error("升级超时",
				zap.String("agentId", agentID),
				zap.Error(err))
			continue
		}

		job.perAgent[agentID].Phase = "success"
		stresslog.Info("升级成功",
			zap.String("agentId", agentID),
			zap.String("version", job.version))

		// 两台 Agent 之间间隔
		if o.rolloutDelay > 0 {
			select {
			case <-time.After(o.rolloutDelay):
			case <-job.cancelCh:
				return
			}
		}
	}
}

func (o *UpgradeOrchestrator) waitForVersion(agentID, version string, job *upgradeJob) error {
	deadline := time.Now().Add(o.perAgentTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-job.cancelCh:
			return fmt.Errorf("cancelled")
		default:
		}

		agent, ok := o.agents.Get(agentID)
		if ok && agent.AppVersion == version && agent.Status != AgentOffline {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timeout waiting for agent %s to reach version %s", agentID, version)
}

func (o *UpgradeOrchestrator) doUpgrade(agentID, version string) error {
	agent, ok := o.agents.Get(agentID)
	if !ok {
		return ErrAgentNotFound
	}
	if agent.Status == AgentOffline {
		return ErrAgentOffline
	}

	binaryURL := fmt.Sprintf("%s/api/binaries/agent-%s.exe", o.publicURL, version)
	meta, ok := o.binaries.Get("agent-" + version + ".exe")
	if !ok {
		return ErrBinaryNotFound
	}

	return o.dispatcher.Upgrade(agent.Address, UpgradeRequest{
		URL:     binaryURL,
		SHA256:  meta.SHA256,
		Version: version,
	})
}
