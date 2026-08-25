package grpcapi

import (
	"errors"
	"strings"
)

func requireAgentID(agentID string) error {
	if strings.TrimSpace(agentID) == "" {
		return errors.New("agentId 不能为空")
	}
	return nil
}
