package template

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"stressbot/admin/apierror"
	json "stressbot/internal/jsonx"
)

// FlowSnapshot 是流程模板库的全量导出：Revision 为内容摘要，
// 用作整体恢复的乐观锁预检。
type FlowSnapshot struct {
	Revision string               `json:"revision"`
	Items    []FlowTemplateDetail `json:"items"`
}

// ReplaceFlowSnapshotRequest 是整体替换流程模板库的请求：
// ExpectedRevision 必须与当前库内容摘要一致，否则替换被拒绝。
type ReplaceFlowSnapshotRequest struct {
	ExpectedRevision string               `json:"expectedRevision"`
	Items            []FlowTemplateDetail `json:"items"`
}

// ReplaceFlowSnapshotResponse 返回替换后流程库的内容摘要与条目数。
type ReplaceFlowSnapshotResponse struct {
	Revision string `json:"revision"`
	Count    int    `json:"count"`
}

type flowSnapshotQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func listFlowDetails(ctx context.Context, q flowSnapshotQuerier) ([]FlowTemplateDetail, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, name, node_count, action_count, created_at, updated_at, flow_json, layout_json
		FROM flow_template ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("list flow snapshot: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := []FlowTemplateDetail{}
	for rows.Next() {
		var (
			item   FlowTemplateDetail
			flow   []byte
			layout []byte
		)
		if err := rows.Scan(
			&item.ID, &item.Name, &item.NodeCount, &item.ActionCount,
			&item.CreatedAt, &item.UpdatedAt, &flow, &layout,
		); err != nil {
			return nil, fmt.Errorf("scan flow snapshot: %w", err)
		}
		item.Flow = json.RawMessage(flow)
		if layout != nil {
			item.Layout = json.RawMessage(layout)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate flow snapshot: %w", err)
	}
	return items, nil
}

// Snapshot 导出流程模板库全量快照（按 id 排序），Revision 为内容摘要。
func (s *FlowTemplateStore) Snapshot(ctx context.Context) (*FlowSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items, err := listFlowDetails(ctx, s.db)
	if err != nil {
		return nil, err
	}
	revision, err := computeFlowSnapshotRevision(items)
	if err != nil {
		return nil, err
	}
	return &FlowSnapshot{Revision: revision, Items: items}, nil
}

// ReplaceSnapshot 在事务内整体替换流程模板库：先校验每条记录（ID 唯一且不超过 32 字符、
// 名称合法、节点/动作数与 flow_json 一致、layout 为合法 JSON），比对 ExpectedRevision
// 通过后 DELETE + 重插全部条目；摘要不一致返回 ErrFlowSnapshotConflict。
func (s *FlowTemplateStore) ReplaceSnapshot(ctx context.Context, req ReplaceFlowSnapshotRequest) (*ReplaceFlowSnapshotResponse, error) {
	items := append([]FlowTemplateDetail(nil), req.Items...)
	if err := validateFlowSnapshotItems(items); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin replace flow snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	current, err := listFlowDetails(ctx, tx)
	if err != nil {
		return nil, err
	}
	currentRevision, err := computeFlowSnapshotRevision(current)
	if err != nil {
		return nil, err
	}
	if req.ExpectedRevision != currentRevision {
		return nil, apierror.ErrFlowSnapshotConflict.WithDetails(map[string]any{
			"expectedRevision": req.ExpectedRevision,
			"actualRevision":   currentRevision,
		})
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM flow_template`); err != nil {
		return nil, fmt.Errorf("delete flow snapshot: %w", err)
	}
	for i := range items {
		item := &items[i]
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO flow_template
				(id, name, flow_json, layout_json, node_count, action_count, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, item.ID, item.Name, []byte(item.Flow), layoutArg(item.Layout),
			item.NodeCount, item.ActionCount, item.CreatedAt, item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("insert flow snapshot: %w", err)
		}
	}
	persisted, err := listFlowDetails(ctx, tx)
	if err != nil {
		return nil, err
	}
	revision, err := computeFlowSnapshotRevision(persisted)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit flow snapshot: %w", err)
	}

	return &ReplaceFlowSnapshotResponse{Revision: revision, Count: len(persisted)}, nil
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
			return apierror.ErrInvalidArgument.WithMessage(fmt.Sprintf("第 %d 个流程 ID 无效", i+1))
		}
		if _, ok := seen[item.ID]; ok {
			return apierror.ErrInvalidArgument.WithMessage("流程 ID 重复：" + item.ID)
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
		if item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
			return apierror.ErrInvalidArgument.WithMessage(fmt.Sprintf("流程 %s 的创建或更新时间无效", item.Name))
		}
		if len(item.Layout) > 0 && !json.Valid(item.Layout) {
			return apierror.ErrInvalidArgument.WithMessage(fmt.Sprintf("流程 %s 的 layout 不是合法 JSON", item.Name))
		}
	}
	return nil
}
