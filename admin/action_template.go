package admin

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"stressbot/engine"
	json "stressbot/utils/jsonx"
)

type ActionTemplate struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Pattern     string          `json:"pattern"`
	Data        json.RawMessage `json:"data"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

type ActionTemplateSaveRequest struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Pattern     string          `json:"pattern"`
	Data        json.RawMessage `json:"data"`
}

var validActionPatterns = map[string]struct{}{
	engine.PatternTCPSend: {}, engine.PatternTCPRequest: {}, engine.PatternTCPConnect: {},
	engine.PatternTCPClose: {}, engine.PatternTCPListen: {}, engine.PatternUDPSend: {},
	engine.PatternUDPRequest: {}, engine.PatternUDPConnect: {}, engine.PatternUDPClose: {},
	engine.PatternUDPListen: {}, engine.PatternHTTPRequest: {}, engine.PatternSetState: {},
	engine.PatternClearState: {}, engine.PatternLua: {},
}

var actionPatternsRequireService = map[string]struct{}{
	engine.PatternTCPSend: {}, engine.PatternTCPRequest: {}, engine.PatternTCPConnect: {},
	engine.PatternTCPClose: {}, engine.PatternTCPListen: {}, engine.PatternUDPSend: {},
	engine.PatternUDPRequest: {}, engine.PatternUDPConnect: {}, engine.PatternUDPClose: {},
	engine.PatternUDPListen: {},
}

var actionPatternsRequireRoute = map[string]struct{}{
	engine.PatternTCPSend: {}, engine.PatternTCPRequest: {}, engine.PatternTCPListen: {},
	engine.PatternUDPSend: {}, engine.PatternUDPRequest: {}, engine.PatternUDPListen: {},
}

func actionPatternIn(pattern string, patterns map[string]struct{}) bool {
	_, ok := patterns[pattern]
	return ok
}

func validateActionTemplateSave(req ActionTemplateSaveRequest) (ActionTemplateSaveRequest, error) {
	name, err := normalizeTemplateName(req.Name)
	if err != nil {
		return req, err
	}
	description, err := normalizeTemplateDescription(req.Description)
	if err != nil {
		return req, err
	}
	if _, ok := validActionPatterns[req.Pattern]; !ok {
		return req, ErrInvalidArgument.WithMessage("动作模板 pattern 无效")
	}
	if _, err := requireJSONObject(req.Data, "动作模板 data"); err != nil {
		return req, err
	}
	var action engine.ActionDef
	if err := json.Unmarshal(req.Data, &action); err != nil {
		return req, ErrInvalidArgument.WithMessage("动作模板 data 无效")
	}
	if action.Pattern != req.Pattern {
		return req, ErrInvalidArgument.WithMessage("动作模板 pattern 与 data.pattern 不一致")
	}
	if actionPatternIn(action.Pattern, actionPatternsRequireService) && strings.TrimSpace(action.Service) == "" {
		return req, ErrInvalidArgument.WithMessage("动作模板 service 不能为空")
	}
	if actionPatternIn(action.Pattern, actionPatternsRequireRoute) && action.Route == nil {
		return req, ErrInvalidArgument.WithMessage("动作模板 route 不能为空")
	}
	if (action.Pattern == engine.PatternTCPConnect || action.Pattern == engine.PatternUDPConnect) && strings.TrimSpace(action.Address) == "" {
		return req, ErrInvalidArgument.WithMessage("动作模板 address 不能为空")
	}
	if (action.Pattern == engine.PatternTCPSend || action.Pattern == engine.PatternUDPSend) && strings.TrimSpace(action.C2SProto) == "" {
		return req, ErrInvalidArgument.WithMessage("动作模板 c2sProto 不能为空")
	}
	if (action.Pattern == engine.PatternTCPRequest || action.Pattern == engine.PatternUDPRequest) && strings.TrimSpace(action.S2CProto) == "" {
		return req, ErrInvalidArgument.WithMessage("动作模板 s2cProto 不能为空")
	}
	if action.Pattern == engine.PatternLua && strings.TrimSpace(action.Script) == "" {
		return req, ErrInvalidArgument.WithMessage("Lua 动作模板 script 不能为空")
	}
	if action.Pattern == engine.PatternClearState && len(action.Keys) == 0 {
		return req, ErrInvalidArgument.WithMessage("清除状态动作模板 keys 不能为空")
	}
	if action.Pattern == engine.PatternHTTPRequest {
		if strings.TrimSpace(action.URL) == "" {
			return req, ErrInvalidArgument.WithMessage("HTTP 动作模板 url 不能为空")
		}
		method := strings.ToUpper(strings.TrimSpace(action.Method))
		if method != "" && method != http.MethodGet && method != http.MethodPost {
			return req, ErrInvalidArgument.WithMessage("HTTP 动作模板 method 仅支持 GET 或 POST")
		}
		contentType := strings.TrimSpace(action.ContentType)
		if contentType != "" && contentType != engine.ContentJSON && contentType != engine.ContentForm {
			return req, ErrInvalidArgument.WithMessage("HTTP 动作模板 contentType 仅支持 json 或 form")
		}
	}
	req.Name, req.Description = name, description
	return req, nil
}

type ActionTemplateStore struct {
	db *sql.DB
	mu sync.RWMutex
}

func NewActionTemplateStore(db *sql.DB) *ActionTemplateStore { return &ActionTemplateStore{db: db} }

func (s *ActionTemplateStore) Create(ctx context.Context, req ActionTemplateSaveRequest) (*ActionTemplate, error) {
	req, err := validateActionTemplateSave(req)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	item := &ActionTemplate{ID: generateID(), Name: req.Name, Description: req.Description, Pattern: req.Pattern, Data: req.Data, CreatedAt: now, UpdatedAt: now}
	_, err = s.db.ExecContext(ctx, `INSERT INTO action_template
		(id, name, description, pattern, data_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, item.ID, item.Name, nullableString(item.Description), item.Pattern, []byte(item.Data), now, now)
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	return item, nil
}

func scanActionTemplate(scanner interface{ Scan(...any) error }) (*ActionTemplate, error) {
	var item ActionTemplate
	var description sql.NullString
	var data []byte
	if err := scanner.Scan(&item.ID, &item.Name, &description, &item.Pattern, &data, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return nil, err
	}
	item.Description = description.String
	item.Data = json.RawMessage(data)
	return &item, nil
}

func (s *ActionTemplateStore) List(ctx context.Context) ([]ActionTemplate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, description, pattern, data_json, created_at, updated_at
		FROM action_template ORDER BY updated_at DESC`)
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	defer rows.Close()
	items := make([]ActionTemplate, 0)
	for rows.Next() {
		item, err := scanActionTemplate(rows)
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

func (s *ActionTemplateStore) get(ctx context.Context, id string) (*ActionTemplate, error) {
	item, err := scanActionTemplate(s.db.QueryRowContext(ctx, `SELECT id, name, description, pattern, data_json, created_at, updated_at FROM action_template WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrActionTemplateNotFound
	}
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	return item, nil
}

func (s *ActionTemplateStore) Get(ctx context.Context, id string) (*ActionTemplate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.get(ctx, id)
}

func (s *ActionTemplateStore) Update(ctx context.Context, id string, req ActionTemplateSaveRequest) (*ActionTemplate, error) {
	req, err := validateActionTemplateSave(req)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.ExecContext(ctx, `UPDATE action_template SET name=?, description=?, pattern=?, data_json=?, updated_at=? WHERE id=?`, req.Name, nullableString(req.Description), req.Pattern, []byte(req.Data), time.Now(), id)
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	if count == 0 {
		return nil, ErrActionTemplateNotFound
	}
	return s.get(ctx, id)
}

func (s *ActionTemplateStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.ExecContext(ctx, `DELETE FROM action_template WHERE id=?`, id)
	if err != nil {
		return mapTemplateWriteError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return mapTemplateWriteError(err)
	}
	if count == 0 {
		return ErrActionTemplateNotFound
	}
	return nil
}

func (s *AdminServer) actionTemplateStore(w http.ResponseWriter) *ActionTemplateStore {
	if s.actionTemplates == nil {
		writeError(w, ErrTemplateLibraryDisabled.WithMessage("服务器未启用模板库"))
		return nil
	}
	return s.actionTemplates
}

func (s *AdminServer) handleListActionTemplates(w http.ResponseWriter, r *http.Request) {
	store := s.actionTemplateStore(w)
	if store == nil {
		return
	}
	items, err := store.List(r.Context())
	if err != nil {
		writeTemplateStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}
func (s *AdminServer) handleCreateActionTemplate(w http.ResponseWriter, r *http.Request) {
	store := s.actionTemplateStore(w)
	if store == nil {
		return
	}
	var req ActionTemplateSaveRequest
	if err := decodeTemplateJSON(w, r, &req, templateCRUDMaxBytes, "动作模板不是合法 JSON"); err != nil {
		writeError(w, err)
		return
	}
	item, err := store.Create(r.Context(), req)
	if err != nil {
		writeTemplateStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}
func (s *AdminServer) handleGetActionTemplate(w http.ResponseWriter, r *http.Request) {
	store := s.actionTemplateStore(w)
	if store == nil {
		return
	}
	item, err := store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeTemplateStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
func (s *AdminServer) handleUpdateActionTemplate(w http.ResponseWriter, r *http.Request) {
	store := s.actionTemplateStore(w)
	if store == nil {
		return
	}
	var req ActionTemplateSaveRequest
	if err := decodeTemplateJSON(w, r, &req, templateCRUDMaxBytes, "动作模板不是合法 JSON"); err != nil {
		writeError(w, err)
		return
	}
	item, err := store.Update(r.Context(), r.PathValue("id"), req)
	if err != nil {
		writeTemplateStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
func (s *AdminServer) handleDeleteActionTemplate(w http.ResponseWriter, r *http.Request) {
	store := s.actionTemplateStore(w)
	if store == nil {
		return
	}
	if err := store.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeTemplateStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
