package template

import (
	"context"
	"database/sql"
	"errors"
	flowdef "stressbot/flow"
	"strings"
	"sync"
	"time"

	"stressbot/admin/apierror"
	json "stressbot/internal/jsonx"
)

type ListenTemplate struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Kind        string          `json:"kind"`
	Data        json.RawMessage `json:"data"`
	DefaultRef  json.RawMessage `json:"defaultRef,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}
type ListenTemplateSaveRequest struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Kind        string          `json:"kind"`
	Data        json.RawMessage `json:"data"`
	DefaultRef  json.RawMessage `json:"defaultRef,omitempty"`
}

func validateListenTemplateSave(req ListenTemplateSaveRequest) (ListenTemplateSaveRequest, error) {
	name, err := normalizeTemplateName(req.Name)
	if err != nil {
		return req, err
	}
	description, err := normalizeTemplateDescription(req.Description)
	if err != nil {
		return req, err
	}
	object, err := requireJSONObject(req.Data, "监听模板 data")
	if err != nil {
		return req, err
	}
	var listen flowdef.ListenDef
	if err := json.Unmarshal(req.Data, &listen); err != nil {
		return req, apierror.ErrInvalidArgument.WithMessage("监听模板 data 无效")
	}
	derived := "silent"
	if _, ok := object["script"]; ok {
		derived = "lua"
	} else if _, ok := object["s2cProto"]; ok {
		derived = "declarative"
	} else if _, ok := object["store"]; ok {
		derived = "declarative"
	}
	if req.Kind != "silent" && req.Kind != "declarative" && req.Kind != "lua" {
		return req, apierror.ErrInvalidArgument.WithMessage("监听模板 kind 无效")
	}
	if req.Kind != derived {
		return req, apierror.ErrInvalidArgument.WithMessage("监听模板 kind 与 data 内容形态不一致")
	}
	if derived == "lua" && strings.TrimSpace(listen.Script) == "" {
		return req, apierror.ErrInvalidArgument.WithMessage("Lua 监听模板 script 不能为空")
	}
	if len(strings.TrimSpace(string(req.DefaultRef))) > 0 {
		object, err := requireJSONObject(req.DefaultRef, "监听模板 defaultRef")
		if err != nil {
			return req, err
		}
		var ref templateDefaultRef
		if err := json.Unmarshal(req.DefaultRef, &ref); err != nil {
			return req, apierror.ErrInvalidArgument.WithMessage("监听模板 defaultRef 无效")
		}
		if strings.TrimSpace(ref.Server) == "" {
			return req, apierror.ErrInvalidArgument.WithMessage("监听模板 defaultRef.server 不能为空")
		}
		route, ok := object["route"]
		if !ok || strings.TrimSpace(string(route)) == "null" {
			return req, apierror.ErrInvalidArgument.WithMessage("监听模板 defaultRef.route 不能为空")
		}
		if ref.QueueSize != nil && *ref.QueueSize <= 0 {
			return req, apierror.ErrInvalidArgument.WithMessage("监听模板 defaultRef.queueSize 必须大于 0")
		}
	}
	req.Name, req.Description = name, description
	return req, nil
}

type ListenTemplateStore struct {
	db     *sql.DB
	mu     sync.RWMutex
	nextID func() string
}

func NewListenTemplateStore(db *sql.DB, nextID func() string) *ListenTemplateStore {
	return &ListenTemplateStore{db: db, nextID: nextID}
}
func (s *ListenTemplateStore) Create(ctx context.Context, req ListenTemplateSaveRequest) (*ListenTemplate, error) {
	req, err := validateListenTemplateSave(req)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	item := &ListenTemplate{ID: s.nextID(), Name: req.Name, Description: req.Description, Kind: req.Kind, Data: req.Data, DefaultRef: req.DefaultRef, CreatedAt: now, UpdatedAt: now}
	_, err = s.db.ExecContext(ctx, `INSERT INTO listen_template (id, name, description, kind, data_json, default_ref_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, item.ID, item.Name, nullableString(item.Description), item.Kind, []byte(item.Data), nullableJSON(item.DefaultRef), now, now)
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	return item, nil
}
func scanListenTemplate(scanner interface{ Scan(...any) error }) (*ListenTemplate, error) {
	var item ListenTemplate
	var description sql.NullString
	var data, defaultRef []byte
	if err := scanner.Scan(&item.ID, &item.Name, &description, &item.Kind, &data, &defaultRef, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return nil, err
	}
	item.Description = description.String
	item.Data = json.RawMessage(data)
	if len(defaultRef) > 0 {
		item.DefaultRef = json.RawMessage(defaultRef)
	}
	return &item, nil
}
func (s *ListenTemplateStore) List(ctx context.Context) ([]ListenTemplate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, description, kind, data_json, default_ref_json, created_at, updated_at FROM listen_template ORDER BY updated_at DESC`)
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	defer rows.Close()
	items := make([]ListenTemplate, 0)
	for rows.Next() {
		item, err := scanListenTemplate(rows)
		if err != nil {
			return nil, mapTemplateWriteError(err)
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, mapTemplateWriteError(err)
	}
	return items, nil
}
func (s *ListenTemplateStore) get(ctx context.Context, id string) (*ListenTemplate, error) {
	item, err := scanListenTemplate(s.db.QueryRowContext(ctx, `SELECT id, name, description, kind, data_json, default_ref_json, created_at, updated_at FROM listen_template WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apierror.ErrListenTemplateNotFound
	}
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	return item, nil
}
func (s *ListenTemplateStore) Get(ctx context.Context, id string) (*ListenTemplate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.get(ctx, id)
}
func (s *ListenTemplateStore) Update(ctx context.Context, id string, req ListenTemplateSaveRequest) (*ListenTemplate, error) {
	req, err := validateListenTemplateSave(req)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.ExecContext(ctx, `UPDATE listen_template SET name=?, description=?, kind=?, data_json=?, default_ref_json=?, updated_at=? WHERE id=?`, req.Name, nullableString(req.Description), req.Kind, []byte(req.Data), nullableJSON(req.DefaultRef), time.Now(), id)
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	if count == 0 {
		return nil, apierror.ErrListenTemplateNotFound
	}
	return s.get(ctx, id)
}
func (s *ListenTemplateStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.ExecContext(ctx, `DELETE FROM listen_template WHERE id=?`, id)
	if err != nil {
		return mapTemplateWriteError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return mapTemplateWriteError(err)
	}
	if count == 0 {
		return apierror.ErrListenTemplateNotFound
	}
	return nil
}
