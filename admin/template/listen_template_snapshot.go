package template

import (
	"bytes"
	"context"
	"database/sql"
	"time"

	"stressbot/admin/apierror"
)

func computeListenSnapshotRevision(items []ListenTemplate) (string, error) {
	return computeTemplateSnapshotRevision(items, func(item ListenTemplate) string { return item.ID })
}
func prepareListenSnapshotItems(current, incoming []ListenTemplate, policy IDPolicy, now time.Time, nextID func() string) ([]ListenTemplate, error) {
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
				return nil, apierror.ErrInvalidArgument.WithMessage("模板快照中的 ID 重复")
			}
			seenIDs[item.ID] = struct{}{}
		}
		if _, exists := seenNames[item.Name]; exists {
			return nil, apierror.ErrInvalidArgument.WithMessage("模板快照中的名称重复")
		}
		seenNames[item.Name] = struct{}{}
		if policy == IDGenerateMissing {
			if item.ID == "" {
				item.ID, item.CreatedAt, item.UpdatedAt = nextID(), now, now
			} else {
				old, exists := currentByID[item.ID]
				if !exists {
					return nil, apierror.ErrInvalidArgument.WithMessage("合并模板 ID 不存在于目标模板库")
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
func (s *ListenTemplateStore) Snapshot(ctx context.Context) (*Snapshot[ListenTemplate], error) {
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
	return &Snapshot[ListenTemplate]{Revision: revision, Items: items}, nil
}
func (s *ListenTemplateStore) ReplaceSnapshot(ctx context.Context, req ReplaceSnapshotRequest[ListenTemplate]) (_ *ReplaceSnapshotResponse[ListenTemplate], retErr error) {
	if _, err := prepareListenSnapshotItems(nil, req.Items, req.IDPolicy, time.Now(), s.nextID); err != nil && req.IDPolicy == IDPreserve {
		return nil, err
	}
	if req.IDPolicy != IDPreserve && req.IDPolicy != IDGenerateMissing {
		return nil, apierror.ErrInvalidArgument.WithMessage("模板快照 idPolicy 无效")
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
		return nil, apierror.ErrTemplateSnapshotConflict.WithMessage("模板库已被其他用户修改，请重新预检").WithDetails(map[string]any{"expectedRevision": req.ExpectedRevision, "actualRevision": currentRevision})
	}
	prepared, err := prepareListenSnapshotItems(current, req.Items, req.IDPolicy, time.Now(), s.nextID)
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
	return &ReplaceSnapshotResponse[ListenTemplate]{Revision: revision, Count: len(persisted), Items: persisted}, nil
}
