package admin

import (
	"fmt"
	"strings"
)

func requireAgentID(agentID string) error {
	if strings.TrimSpace(agentID) == "" {
		return fmt.Errorf("agentId 不能为空")
	}
	return nil
}
