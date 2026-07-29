package admin

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"time"
)

func computeListenSnapshotRevision(items []ListenTemplate) (string, error) {
	return computeTemplateSnapshotRevision(items, func(item ListenTemplate) string { return item.ID })
}
func prepareListenSnapshotItems(current, incoming []ListenTemplate, policy TemplateIDPolicy, now time.Time, nextID func() string) ([]ListenTemplate, error) {
	currentByID := make(map[string]ListenTemplate, len(current))
	for _, item := range current {
		currentByID[item.ID] = item
	}
	seenIDs, seenNames := map[string]struct{}{}, map[string]struct{}{}
	prepared := make([]ListenTemplate, 0, len(incoming))
	for _, item := range incoming {
		normalized, err := validateListenTemplateSave(ListenTemplateSaveRequest{Name: item.Name, Description: item.Description, Kind: item.Kind, Data: item.Data, DefaultRef: item.DefaultRef})
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
				if listenTemplateEditableEqual(old, item) {
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
func listenTemplateEditableEqual(a, b ListenTemplate) bool {
	return a.Name == b.Name && a.Description == b.Description && a.Kind == b.Kind && bytes.Equal(a.Data, b.Data) && bytes.Equal(a.DefaultRef, b.DefaultRef)
}

type listenSnapshotQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readListenSnapshotRows(ctx context.Context, q listenSnapshotQuerier) ([]ListenTemplate, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name, description, kind, data_json, default_ref_json, created_at, updated_at FROM listen_template ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ListenTemplate, 0)
	for rows.Next() {
		item, err := scanListenTemplate(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}
func (s *ListenTemplateStore) Snapshot(ctx context.Context) (*TemplateSnapshot[ListenTemplate], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items, err := readListenSnapshotRows(ctx, s.db)
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	revision, err := computeListenSnapshotRevision(items)
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	return &TemplateSnapshot[ListenTemplate]{Revision: revision, Items: items}, nil
}
func (s *ListenTemplateStore) ReplaceSnapshot(ctx context.Context, req ReplaceTemplateSnapshotRequest[ListenTemplate]) (_ *ReplaceTemplateSnapshotResponse[ListenTemplate], retErr error) {
	if _, err := prepareListenSnapshotItems(nil, req.Items, req.IDPolicy, time.Now(), generateID); err != nil && req.IDPolicy == TemplateIDPreserve {
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
	current, err := readListenSnapshotRows(ctx, tx)
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	currentRevision, err := computeListenSnapshotRevision(current)
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	if req.ExpectedRevision != currentRevision {
		return nil, ErrTemplateSnapshotConflict.WithMessage("模板库已被其他用户修改，请重新预检").WithDetails(map[string]any{"expectedRevision": req.ExpectedRevision, "actualRevision": currentRevision})
	}
	prepared, err := prepareListenSnapshotItems(current, req.Items, req.IDPolicy, time.Now(), generateID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM listen_template`); err != nil {
		return nil, mapTemplateWriteError(err)
	}
	for _, item := range prepared {
		if _, err := tx.ExecContext(ctx, `INSERT INTO listen_template (id, name, description, kind, data_json, default_ref_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, item.ID, item.Name, nullableString(item.Description), item.Kind, []byte(item.Data), nullableJSON(item.DefaultRef), item.CreatedAt, item.UpdatedAt); err != nil {
			return nil, mapTemplateWriteError(err)
		}
	}
	persisted, err := readListenSnapshotRows(ctx, tx)
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	revision, err := computeListenSnapshotRevision(persisted)
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, mapTemplateWriteError(err)
	}
	return &ReplaceTemplateSnapshotResponse[ListenTemplate]{Revision: revision, Count: len(persisted), Items: persisted}, nil
}
func (s *AdminServer) handleGetListenTemplateSnapshot(w http.ResponseWriter, r *http.Request) {
	store := s.listenTemplateStore(w)
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
func (s *AdminServer) handleReplaceListenTemplateSnapshot(w http.ResponseWriter, r *http.Request) {
	store := s.listenTemplateStore(w)
	if store == nil {
		return
	}
	var req ReplaceTemplateSnapshotRequest[ListenTemplate]
	if err := decodeTemplateJSON(w, r, &req, templateSnapshotMaxBytes, "监听模板快照不是合法 JSON"); err != nil {
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
