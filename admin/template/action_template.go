// Package template 实现共享模板库的 MySQL 存储：动作/监听/流程三类模板的
// CRUD、保存前校验与带乐观锁的快照导出/整体恢复。
package template

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	flowdef "stressbot/flow"
	"strings"
	"sync"
	"time"

	"stressbot/admin/apierror"
	json "stressbot/internal/jsonx"
)

// ActionTemplate 是可复用的动作定义模板：Pattern 限定 14 种动作形态，
// Data 为对应 ActionDef 的 JSON 对象。
type ActionTemplate struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Pattern     string          `json:"pattern"`
	Data        json.RawMessage `json:"data"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

// ActionTemplateSaveRequest 是创建/更新动作模板的请求体，保存前经
// validateActionTemplateSave 归一化并按 pattern 校验必填字段（service/route/address 等）。
type ActionTemplateSaveRequest struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Pattern     string          `json:"pattern"`
	Data        json.RawMessage `json:"data"`
}

var validActionPatterns = map[string]struct{}{
	flowdef.PatternTCPSend: {}, flowdef.PatternTCPRequest: {}, flowdef.PatternTCPConnect: {},
	flowdef.PatternTCPClose: {}, flowdef.PatternTCPListen: {}, flowdef.PatternUDPSend: {},
	flowdef.PatternUDPRequest: {}, flowdef.PatternUDPConnect: {}, flowdef.PatternUDPClose: {},
	flowdef.PatternUDPListen: {}, flowdef.PatternHTTPRequest: {}, flowdef.PatternSetState: {},
	flowdef.PatternClearState: {}, flowdef.PatternLua: {},
}

var actionPatternsRequireService = map[string]struct{}{
	flowdef.PatternTCPSend: {}, flowdef.PatternTCPRequest: {}, flowdef.PatternTCPConnect: {},
	flowdef.PatternTCPClose: {}, flowdef.PatternTCPListen: {}, flowdef.PatternUDPSend: {},
	flowdef.PatternUDPRequest: {}, flowdef.PatternUDPConnect: {}, flowdef.PatternUDPClose: {},
	flowdef.PatternUDPListen: {},
}

var actionPatternsRequireRoute = map[string]struct{}{
	flowdef.PatternTCPSend: {}, flowdef.PatternTCPRequest: {}, flowdef.PatternTCPListen: {},
	flowdef.PatternUDPSend: {}, flowdef.PatternUDPRequest: {}, flowdef.PatternUDPListen: {},
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
		return req, apierror.ErrInvalidArgument.WithMessage("动作模板 pattern 无效")
	}
	if _, err := requireJSONObject(req.Data, "动作模板 data"); err != nil {
		return req, err
	}
	var action flowdef.ActionDef
	if err := json.Unmarshal(req.Data, &action); err != nil {
		return req, apierror.ErrInvalidArgument.WithMessage("动作模板 data 无效")
	}
	if action.Pattern != req.Pattern {
		return req, apierror.ErrInvalidArgument.WithMessage("动作模板 pattern 与 data.pattern 不一致")
	}
	if actionPatternIn(action.Pattern, actionPatternsRequireService) && strings.TrimSpace(action.Service) == "" {
		return req, apierror.ErrInvalidArgument.WithMessage("动作模板 service 不能为空")
	}
	if actionPatternIn(action.Pattern, actionPatternsRequireRoute) && action.Route == nil {
		return req, apierror.ErrInvalidArgument.WithMessage("动作模板 route 不能为空")
	}
	if (action.Pattern == flowdef.PatternTCPConnect || action.Pattern == flowdef.PatternUDPConnect) && strings.TrimSpace(action.Address) == "" {
		return req, apierror.ErrInvalidArgument.WithMessage("动作模板 address 不能为空")
	}
	if (action.Pattern == flowdef.PatternTCPSend || action.Pattern == flowdef.PatternUDPSend) && strings.TrimSpace(action.C2SProto) == "" {
		return req, apierror.ErrInvalidArgument.WithMessage("动作模板 c2sProto 不能为空")
	}
	if (action.Pattern == flowdef.PatternTCPRequest || action.Pattern == flowdef.PatternUDPRequest) && strings.TrimSpace(action.S2CProto) == "" {
		return req, apierror.ErrInvalidArgument.WithMessage("动作模板 s2cProto 不能为空")
	}
	if action.Pattern == flowdef.PatternLua && strings.TrimSpace(action.Script) == "" {
		return req, apierror.ErrInvalidArgument.WithMessage("Lua 动作模板 script 不能为空")
	}
	if action.Pattern == flowdef.PatternClearState && len(action.Keys) == 0 {
		return req, apierror.ErrInvalidArgument.WithMessage("清除状态动作模板 keys 不能为空")
	}
	if action.Pattern == flowdef.PatternHTTPRequest {
		if strings.TrimSpace(action.URL) == "" {
			return req, apierror.ErrInvalidArgument.WithMessage("HTTP 动作模板 url 不能为空")
		}
		method := strings.ToUpper(strings.TrimSpace(action.Method))
		if method != "" && method != http.MethodGet && method != http.MethodPost {
			return req, apierror.ErrInvalidArgument.WithMessage("HTTP 动作模板 method 仅支持 GET 或 POST")
		}
		contentType := strings.TrimSpace(action.ContentType)
		if contentType != "" && contentType != flowdef.ContentJSON && contentType != flowdef.ContentForm {
			return req, apierror.ErrInvalidArgument.WithMessage("HTTP 动作模板 contentType 仅支持 json 或 form")
		}
	}
	req.Name, req.Description = name, description
	return req, nil
}

// ActionTemplateStore 是动作模板的 MySQL 存取层，内置 ID 生成器与读写锁。
type ActionTemplateStore struct {
	db     *sql.DB
	mu     sync.RWMutex
	nextID func() string
}

// NewActionTemplateStore 创建动作模板存储；nextID 供创建与快照合并生成新模板 ID。
func NewActionTemplateStore(db *sql.DB, nextID func() string) *ActionTemplateStore {
	return &ActionTemplateStore{db: db, nextID: nextID}
}

// Create 校验并插入一条动作模板，重名（唯一键冲突）映射为 ErrTemplateNameConflict。
func (s *ActionTemplateStore) Create(ctx context.Context, req ActionTemplateSaveRequest) (*ActionTemplate, error) {
	req, err := validateActionTemplateSave(req)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	item := &ActionTemplate{ID: s.nextID(), Name: req.Name, Description: req.Description, Pattern: req.Pattern, Data: req.Data, CreatedAt: now, UpdatedAt: now}
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

// List 按 updated_at 倒序列出全部动作模板。
func (s *ActionTemplateStore) List(ctx context.Context) ([]ActionTemplate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, description, pattern, data_json, created_at, updated_at
		FROM action_template ORDER BY updated_at DESC`)
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	defer func() { _ = rows.Close() }()
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
		return nil, apierror.ErrActionTemplateNotFound
	}
	if err != nil {
		return nil, mapTemplateWriteError(err)
	}
	return item, nil
}

// Get 按 ID 查询单个动作模板，缺失时返回 ErrActionTemplateNotFound。
func (s *ActionTemplateStore) Get(ctx context.Context, id string) (*ActionTemplate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.get(ctx, id)
}

// Update 校验并覆盖指定动作模板的可编辑字段，返回更新后的记录；
// 目标不存在时返回 ErrActionTemplateNotFound。
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
		return nil, apierror.ErrActionTemplateNotFound
	}
	return s.get(ctx, id)
}

// Delete 按 ID 删除动作模板；目标不存在时返回 ErrActionTemplateNotFound。
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
		return apierror.ErrActionTemplateNotFound
	}
	return nil
}
