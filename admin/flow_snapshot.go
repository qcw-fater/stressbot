package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	json "stressbot/utils/jsonx"
)

type FlowSnapshot struct {
	Revision string               `json:"revision"`
	Items    []FlowTemplateDetail `json:"items"`
}

type ReplaceFlowSnapshotRequest struct {
	ExpectedRevision string               `json:"expectedRevision"`
	Items            []FlowTemplateDetail `json:"items"`
}

type ReplaceFlowSnapshotResponse struct {
	Revision string `json:"revision"`
	Count    int    `json:"count"`
}

func computeFlowSnapshotRevision(items []FlowTemplateDetail) (string, error) {
	stable := append([]FlowTemplateDetail(nil), items...)
	sort.Slice(stable, func(i, j int) bool { return stable[i].ID < stable[j].ID })

	b, err := json.Marshal(stable)
	if err != nil {
		return "", fmt.Errorf("marshal flow snapshot: %w", err)
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validateFlowSnapshotItems(items []FlowTemplateDetail) error {
	seen := make(map[string]struct{}, len(items))
	for i := range items {
		item := &items[i]
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" || len(item.ID) > 32 {
			return ErrInvalidArgument.WithMessage(fmt.Sprintf("第 %d 个流程 ID 无效", i+1))
		}
		if _, ok := seen[item.ID]; ok {
			return ErrInvalidArgument.WithMessage(fmt.Sprintf("流程 ID 重复：%s", item.ID))
		}
		seen[item.ID] = struct{}{}

		name, err := validateFlowTemplateName(item.Name)
		if err != nil {
			return err
		}
		item.Name = name

		nodeCount, actionCount, err := countFlowNodesActions(item.Flow)
		if err != nil {
			return err
		}
		item.NodeCount = nodeCount
		item.ActionCount = actionCount
		if len(item.Layout) > 0 && !json.Valid(item.Layout) {
			return ErrInvalidArgument.WithMessage(fmt.Sprintf("流程 %s 的 layout 不是合法 JSON", item.Name))
		}
	}
	return nil
}
