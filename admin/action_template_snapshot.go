package admin

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"time"
)

func computeActionSnapshotRevision(items []ActionTemplate) (string, error) {
	return computeTemplateSnapshotRevision(items, func(item ActionTemplate) string { return item.ID })
}

func prepareActionSnapshotItems(current, incoming []ActionTemplate, policy TemplateIDPolicy, now time.Time, nextID func() string) ([]ActionTemplate, error) {
	currentByID := make(map[string]ActionTemplate, len(current))
	for _, item := range current {
		currentByID[item.ID] = item
	}
	seenIDs, seenNames := map[string]struct{}{}, map[string]struct{}{}
	prepared := make([]ActionTemplate, 0, len(incoming))
	for _, item := range incoming {
		normalized, err := validateActionTemplateSave(ActionTemplateSaveRequest{Name: item.Name, Description: item.Description, Pattern: item.Pattern, Data: item.Data})
		if err != nil {
			return nil, err
		}
		item.Name, item.Description = normalized.Name, normalized.Description
		if err := validateTemplateSnapshotIdentity(item.ID, item.Name, policy, item.CreatedAt, item.UpdatedAt); err != nil {
			return nil, err
		}
		if item.ID != "" {
			if _, exists := seenIDs[item.ID]; exists {
				return nil, ErrInvalidArgument.WithMessage("模板快照中的 ID 重复")
			}
			seenIDs[item.ID] = struct{}{}
		}
		if _, exists := seenNames[item.Name]; exists {
			return nil, ErrInvalidArgument.WithMessage("模板快照中的名称重复")
		}
		seenNames[item.Name] = struct{}{}
		if policy == TemplateIDGenerateMissing {
			if item.ID == "" {
				item.ID, item.CreatedAt, item.UpdatedAt = nextID(), now, now
			} else {
				old, exists := currentByID[item.ID]
				if !exists {
					return nil, ErrInvalidArgument.WithMessage("合并模板 ID 不存在于目标模板库")
				}
				item.CreatedAt = old.CreatedAt
				if actionTemplateEditableEqual(old, item) {
					item.UpdatedAt = old.UpdatedAt
				} else {
					item.UpdatedAt = now
				}
			}
		}
		prepared = append(prepared, item)
	}
	return prepared, nil
}

func actionTemplateEditableEqual(a, b ActionTemplate) bool {
	return a.Name == b.Name && a.Description == b.Description && a.Pattern == b.Pattern && bytes.Equal(a.Data, b.Data)
}

type actionSnapshotQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readActionSnapshotRows(ctx context.Context, q actionSnapshotQuerier) ([]ActionTemplate, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name, description, pattern, data_json, created_at, updated_at FROM action_template ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ActionTemplate, 0)
	for rows.Next() {
		item, err := scanActionTemplate(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

func (s *ActionTemplateStore) Snapshot(ctx context.Context) (*TemplateSnapshot[ActionTemplate], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items, err := readActionSnapshotRows(ctx, s.db)
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	revision, err := computeActionSnapshotRevision(items)
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	return &TemplateSnapshot[ActionTemplate]{Revision: revision, Items: items}, nil
}

func (s *ActionTemplateStore) ReplaceSnapshot(ctx context.Context, req ReplaceTemplateSnapshotRequest[ActionTemplate]) (_ *ReplaceTemplateSnapshotResponse[ActionTemplate], retErr error) {
	if _, err := prepareActionSnapshotItems(nil, req.Items, req.IDPolicy, time.Now(), generateID); err != nil && req.IDPolicy == TemplateIDPreserve {
		return nil, err
	}
	if req.IDPolicy != TemplateIDPreserve && req.IDPolicy != TemplateIDGenerateMissing {
		return nil, ErrInvalidArgument.WithMessage("模板快照 idPolicy 无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	defer func() {
		if retErr != nil {
			_ = tx.Rollback()
		}
	}()
	current, err := readActionSnapshotRows(ctx, tx)
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	currentRevision, err := computeActionSnapshotRevision(current)
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	if req.ExpectedRevision != currentRevision {
		return nil, ErrTemplateSnapshotConflict.WithMessage("模板库已被其他用户修改，请重新预检").WithDetails(map[string]any{"expectedRevision": req.ExpectedRevision, "actualRevision": currentRevision})
	}
	prepared, err := prepareActionSnapshotItems(current, req.Items, req.IDPolicy, time.Now(), generateID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM action_template`); err != nil {
		return nil, mapTemplateWriteError(err)
	}
	for _, item := range prepared {
		if _, err := tx.ExecContext(ctx, `INSERT INTO action_template (id, name, description, pattern, data_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, item.ID, item.Name, nullableString(item.Description), item.Pattern, []byte(item.Data), item.CreatedAt, item.UpdatedAt); err != nil {
			return nil, mapTemplateWriteError(err)
		}
	}
	persisted, err := readActionSnapshotRows(ctx, tx)
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	revision, err := computeActionSnapshotRevision(persisted)
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, mapTemplateWriteError(err)
	}
	return &ReplaceTemplateSnapshotResponse[ActionTemplate]{Revision: revision, Count: len(persisted), Items: persisted}, nil
}

func (s *AdminServer) handleGetActionTemplateSnapshot(w http.ResponseWriter, r *http.Request) {
	store := s.actionTemplateStore(w)
	if store == nil {
		return
	}
	snapshot, err := store.Snapshot(r.Context())
	if err != nil {
		writeTemplateStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}
func (s *AdminServer) handleReplaceActionTemplateSnapshot(w http.ResponseWriter, r *http.Request) {
	store := s.actionTemplateStore(w)
	if store == nil {
		return
	}
	var req ReplaceTemplateSnapshotRequest[ActionTemplate]
	if err := decodeTemplateJSON(w, r, &req, templateSnapshotMaxBytes, "动作模板快照不是合法 JSON"); err != nil {
		writeError(w, err)
		return
	}
	result, err := store.ReplaceSnapshot(r.Context(), req)
	if err != nil {
		writeTemplateStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
