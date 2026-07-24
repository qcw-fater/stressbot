package admin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
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
	defer rows.Close()

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
	defer tx.Rollback()

	current, err := listFlowDetails(ctx, tx)
	if err != nil {
		return nil, err
	}
	currentRevision, err := computeFlowSnapshotRevision(current)
	if err != nil {
		return nil, err
	}
	if req.ExpectedRevision != currentRevision {
		return nil, ErrFlowSnapshotConflict.WithDetails(map[string]any{
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
		if item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
			return ErrInvalidArgument.WithMessage(fmt.Sprintf("流程 %s 的创建或更新时间无效", item.Name))
		}
		if len(item.Layout) > 0 && !json.Valid(item.Layout) {
			return ErrInvalidArgument.WithMessage(fmt.Sprintf("流程 %s 的 layout 不是合法 JSON", item.Name))
		}
	}
	return nil
}

func (s *AdminServer) handleGetFlowSnapshot(w http.ResponseWriter, r *http.Request) {
	if s.flows == nil {
		writeError(w, ErrFlowLibraryDisabled.WithMessage("服务器未启用流程库"))
		return
	}
	snapshot, err := s.flows.Snapshot(r.Context())
	if err != nil {
		writeError(w, ErrInternal.WithMessage(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *AdminServer) handleReplaceFlowSnapshot(w http.ResponseWriter, r *http.Request) {
	if s.flows == nil {
		writeError(w, ErrFlowLibraryDisabled.WithMessage("服务器未启用流程库"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 50<<20)
	var req ReplaceFlowSnapshotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, ErrInvalidArgument.WithMessage("备份中的流程快照不是合法 JSON"))
		return
	}
	resp, err := s.flows.ReplaceSnapshot(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
