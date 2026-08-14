package template

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"stressbot/admin/apierror"
	"stressbot/config/validation"
	json "stressbot/internal/jsonx"
)

// flowTemplateNameMax 流程模板名称最大长度（与前端 FLOW_NAME_MAX_LENGTH 一致）。
const flowTemplateNameMax = 80

// FlowTemplateStore 流程模板库存储。
// db 由 Admin Server 统一管理（共享全局 MySQL 实例），本结构不负责 Close。
// 当全局 MySQL 未配置时 Admin Server 不装配该 Store，相关接口返回 FLOW_LIBRARY_DISABLED。
type FlowTemplateStore struct {
	db     *sql.DB
	mu     sync.RWMutex
	nextID func() string
}

// NewFlowTemplateStore 创建流程模板库存储。db 必须非 nil。
func NewFlowTemplateStore(db *sql.DB, nextID func() string) *FlowTemplateStore {
	return &FlowTemplateStore{db: db, nextID: nextID}
}

// FlowTemplateSummary 流程模板摘要（列表用，不含 flow/layout，避免传输大字段）。
type FlowTemplateSummary struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	NodeCount   int       `json:"nodeCount"`
	ActionCount int       `json:"actionCount"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// FlowTemplateDetail 流程模板详情（含完整 flow/layout，供打开与启动读取）。
type FlowTemplateDetail struct {
	FlowTemplateSummary
	Flow   json.RawMessage `json:"flow"`
	Layout json.RawMessage `json:"layout,omitempty"`
}

// FlowTemplateSaveRequest 创建/覆盖流程模板请求。
type FlowTemplateSaveRequest struct {
	Name   string          `json:"name"`
	Flow   json.RawMessage `json:"flow"`
	Layout json.RawMessage `json:"layout,omitempty"`
}

// validateFlowTemplateName 校验名称：去首尾空白、非空、不超过上限。
func validateFlowTemplateName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		return "", apierror.ErrInvalidArgument.WithMessage("流程名称不能为空")
	}
	if len(n) > flowTemplateNameMax {
		return "", apierror.ErrInvalidArgument.WithMessage(fmt.Sprintf("流程名称不能超过 %d 个字符", flowTemplateNameMax))
	}
	return n, nil
}

// countFlowNodesActions 从 flow_json 统计 node/action 数量。
// flow 结构：{ nodes: {id:...}, actions: {id:...}, ... }，nodes/actions 为 map。
func countFlowNodesActions(flowJSON []byte) (nodeCount, actionCount int, err error) {
	if err := validation.ValidateFlow(flowJSON); err != nil {
		return 0, 0, apierror.ErrInvalidArgument.WithMessage(err.Error())
	}
	var parsed struct {
		Nodes   map[string]json.RawMessage `json:"nodes"`
		Actions map[string]json.RawMessage `json:"actions"`
	}
	if err := json.Unmarshal(flowJSON, &parsed); err != nil {
		return 0, 0, apierror.ErrInvalidArgument.WithMessage("flow 不是合法 JSON 对象")
	}
	return len(parsed.Nodes), len(parsed.Actions), nil
}

// layoutArg 把请求中的 layout 转为可写入 BLOB 的参数（空 → NULL）。
func layoutArg(layout json.RawMessage) any {
	if len(strings.TrimSpace(string(layout))) == 0 {
		return nil
	}
	return []byte(layout)
}

// Create 创建流程模板。
func (s *FlowTemplateStore) Create(ctx context.Context, req FlowTemplateSaveRequest) (*FlowTemplateDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	name, err := validateFlowTemplateName(req.Name)
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(req.Flow))) == 0 {
		return nil, apierror.ErrInvalidArgument.WithMessage("flow 不能为空")
	}
	nodeCount, actionCount, err := countFlowNodesActions(req.Flow)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	id := s.nextID()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO flow_template (id, name, flow_json, layout_json, node_count, action_count, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, id, name, []byte(req.Flow), layoutArg(req.Layout), nodeCount, actionCount, now, now); err != nil {
		return nil, fmt.Errorf("insert flow_template: %w", err)
	}
	return &FlowTemplateDetail{
		FlowTemplateSummary: FlowTemplateSummary{
			ID: id, Name: name, NodeCount: nodeCount, ActionCount: actionCount,
			CreatedAt: now, UpdatedAt: now,
		},
		Flow:   req.Flow,
		Layout: req.Layout,
	}, nil
}

// List 列出所有流程模板摘要（按更新时间倒序）。
func (s *FlowTemplateStore) List(ctx context.Context) ([]FlowTemplateSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, node_count, action_count, created_at, updated_at
		FROM flow_template ORDER BY updated_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list flow_template: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := []FlowTemplateSummary{}
	for rows.Next() {
		var it FlowTemplateSummary
		if err := rows.Scan(&it.ID, &it.Name, &it.NodeCount, &it.ActionCount, &it.CreatedAt, &it.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan flow_template: %w", err)
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate flow_template: %w", err)
	}
	return items, nil
}

// Get 查询单个流程模板完整内容。
func (s *FlowTemplateStore) Get(ctx context.Context, id string) (*FlowTemplateDetail, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.get(ctx, id)
}

func (s *FlowTemplateStore) get(ctx context.Context, id string) (*FlowTemplateDetail, error) {
	var (
		d      FlowTemplateDetail
		flow   []byte
		layout []byte
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, node_count, action_count, created_at, updated_at, flow_json, layout_json
		FROM flow_template WHERE id = ?
	`, id).Scan(&d.ID, &d.Name, &d.NodeCount, &d.ActionCount, &d.CreatedAt, &d.UpdatedAt, &flow, &layout)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apierror.ErrFlowTemplateNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get flow_template: %w", err)
	}
	d.Flow = json.RawMessage(flow)
	if layout != nil {
		d.Layout = json.RawMessage(layout)
	}
	return &d, nil
}

// Update 覆盖流程模板（name 必填）。flow 非空时连同 layout 一起覆盖并重新计数；
// flow 为空时仅重命名。
func (s *FlowTemplateStore) Update(ctx context.Context, id string, req FlowTemplateSaveRequest) (*FlowTemplateDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	name, err := validateFlowTemplateName(req.Name)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if len(strings.TrimSpace(string(req.Flow))) > 0 {
		nodeCount, actionCount, cerr := countFlowNodesActions(req.Flow)
		if cerr != nil {
			return nil, cerr
		}
		if _, err := s.db.ExecContext(ctx, `
			UPDATE flow_template
			SET name=?, flow_json=?, layout_json=?, node_count=?, action_count=?, updated_at=?
			WHERE id=?
		`, name, []byte(req.Flow), layoutArg(req.Layout), nodeCount, actionCount, now, id); err != nil {
			return nil, fmt.Errorf("update flow_template: %w", err)
		}
	} else {
		if _, err := s.db.ExecContext(ctx, `UPDATE flow_template SET name=?, updated_at=? WHERE id=?`, name, now, id); err != nil {
			return nil, fmt.Errorf("rename flow_template: %w", err)
		}
	}
	// UPDATE 命中 0 行（id 不存在）时由 Get 翻译为 FLOW_TEMPLATE_NOT_FOUND。
	return s.get(ctx, id)
}

// Delete 删除流程模板。模板删除只影响库本身，不级联历史（flow_template_id 为逻辑外键）。
func (s *FlowTemplateStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.ExecContext(ctx, `DELETE FROM flow_template WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete flow_template: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete flow_template rows affected: %w", err)
	}
	if n == 0 {
		return apierror.ErrFlowTemplateNotFound
	}
	return nil
}

// ── HTTP handlers（/sbot/flows）──
//
// flows == nil（未配置 MySQL）时统一返回 FLOW_LIBRARY_DISABLED，
// 不影响非流程库功能。
