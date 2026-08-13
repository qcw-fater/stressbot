package agent

import (
	"context"

	"stressbot/agent/session"
)

func (a *Agent) connectionSupervisor(ctx context.Context) error {
	return session.RunSupervisor(ctx, session.SupervisorConfig{
		Address:              a.cfg.AdminAddress,
		ReconnectInterval:    a.cfg.ReconnectInterval,
		ReconnectMaxInterval: a.cfg.ReconnectMaxInterval,
		ReconnectMaxRetries:  a.cfg.ReconnectMaxRetries,
		MaxMessageSize:       maxControlMessageSize,
	}, a.runConnection)
}
