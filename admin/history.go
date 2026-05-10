package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"stressbot/monitor"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// allowedOrderBy 白名单，防止 SQL 注入。
var allowedOrderBy = map[string]bool{
	"created_at DESC":          true,
	"created_at ASC":           true,
	"started_at DESC":          true,
	"started_at ASC":           true,
	"duration_sec DESC":        true,
	"duration_sec ASC":         true,
	"total_bots DESC":          true,
	"total_bots ASC":           true,
	"state":                    true,
	"name ASC":                 true,
	"name DESC":                true,
}

// HistoryStore MySQL 历史归档存储。
// 当 cfg.Enabled=false 时，NewHistoryStore 仍然返回实例但底层无 DB 连接，
// 由 admin.go 判断 history==nil 来跳过。
type HistoryStore struct {
	cfg    HistoryConfig
	db     *sql.DB
	prune  time.Duration
	cancel context.CancelFunc
}

func NewHistoryStore(cfg HistoryConfig) (*HistoryStore, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	db, err := openDB(cfg.MySQL)
	if err != nil {
		return nil, fmt.Errorf("connect mysql: %w", err)
	}

	h := &HistoryStore{cfg: cfg, db: db}
	if err := h.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}

	if cfg.RetentionDays <= 0 {
		cfg.RetentionDays = 90
	}
	h.prune = 24 * time.Hour

	stresslog.Info("HistoryStore 已连接 MySQL",
		zap.String("dsn", maskDSN(cfg.MySQL.DSN)),
		zap.Int("retentionDays", cfg.RetentionDays))

	return h, nil
}

func openDB(cfg MySQLConfig) (*sql.DB, error) {
	db, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		return nil, err
	}
	if cfg.MaxOpenConns > 0 {
		db.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		db.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if d, err := time.ParseDuration(cfg.ConnMaxLifetime); err == nil && d > 0 {
		db.SetConnMaxLifetime(d)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (h *HistoryStore) initSchema() error {
	for _, ddl := range allDDL {
		if _, err := h.db.Exec(ddl); err != nil {
			return err
		}
	}
	return nil
}

// Archive 将终态任务归档到 MySQL（事务写入 5 张表）。
func (h *HistoryStore) Archive(task *Task, finalStress *monitor.CollectorSnapshot, finalSys ClusterSystemSnapshot) error {
	if h.db == nil {
		return nil
	}

	tx, err := h.db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// 1. task_history
	durationSec := 0
	if task.StartedAt != nil && task.StoppedAt != nil {
		durationSec = int(task.StoppedAt.Sub(*task.StartedAt).Seconds())
	}
	agentCount := len(task.Assignments)
	var tags []string
	if task.Config.RobotConfig.DebugMode {
		tags = append(tags, "debug")
	}
	tagsJSON, _ := json.Marshal(tags)
	if tagsJSON == nil {
		tagsJSON = []byte("[]")
	}
	summaryJSON, _ := json.Marshal(buildConfigSummary(task))

	_, err = tx.Exec(`
		INSERT INTO task_history (id, name, state, total_bots, agent_count,
			created_at, started_at, stopped_at, duration_sec, error_msg,
			starred, tags, note, config_summary)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, '', ?)
		ON DUPLICATE KEY UPDATE
			state=VALUES(state), stopped_at=VALUES(stopped_at),
			duration_sec=VALUES(duration_sec), error_msg=VALUES(error_msg)
	`,
		task.ID, task.Name, string(task.State), task.TotalBots, agentCount,
		task.CreatedAt, task.StartedAt, task.StoppedAt, durationSec, task.ErrorMsg,
		tagsJSON, summaryJSON,
	)
	if err != nil {
		return fmt.Errorf("insert task_history: %w", err)
	}

	// 2. task_assignment
	for _, a := range task.Assignments {
		_, err = tx.Exec(`
			INSERT INTO task_assignment (task_id, agent_id, start_number, total_bots)
			VALUES (?, ?, ?, ?)
		`, task.ID, a.AgentID, a.StartNumber, a.TotalBots)
		if err != nil {
			return fmt.Errorf("insert task_assignment: %w", err)
		}
	}

	// 3. task_report
	for agentID, report := range task.Reports {
		snapJSON, _ := json.Marshal(report.FinalSnapshot)
		_, err = tx.Exec(`
			INSERT INTO task_report (task_id, agent_id, agent_name, result, error_msg, finished_at, final_snapshot)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, task.ID, agentID, "", string(report.Result), report.ErrorMsg, report.FinishedAt, snapJSON)
		if err != nil {
			return fmt.Errorf("insert task_report: %w", err)
		}
	}

	// 4. task_aggregated
	stressJSON, _ := json.Marshal(finalStress)
	sysJSON, _ := json.Marshal(finalSys)
	_, err = tx.Exec(`
		INSERT INTO task_aggregated (task_id, final_stress, final_system)
		VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE final_stress=VALUES(final_stress), final_system=VALUES(final_system)
	`, task.ID, stressJSON, sysJSON)
	if err != nil {
		return fmt.Errorf("insert task_aggregated: %w", err)
	}

	// 5. task_config_archive
	flowJSON := task.Config.FlowJSON
	headerJSON := task.Config.HeaderJSON
	protoFilesJSON, _ := json.Marshal(task.Config.ProtoFiles)
	luaScriptsJSON, _ := json.Marshal(task.Config.LuaScripts)
	robotCfgJSON, _ := json.Marshal(task.Config.RobotConfig)
	_, err = tx.Exec(`
		INSERT INTO task_config_archive (task_id, flow_json, header_json, proto_files, lua_scripts, robot_config)
		VALUES (?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE flow_json=VALUES(flow_json)
	`, task.ID, flowJSON, headerJSON, protoFilesJSON, luaScriptsJSON, robotCfgJSON)
	if err != nil {
		return fmt.Errorf("insert task_config_archive: %w", err)
	}

	return tx.Commit()
}

// List 分页查询历史记录。
func (h *HistoryStore) List(filter HistoryFilter) (*HistoryListResponse, error) {
	if h.db == nil {
		return &HistoryListResponse{Items: []HistoryRecord{}}, nil
	}

	where, args := buildListWhere(filter)
	orderBy := "created_at DESC"
	if allowedOrderBy[filter.OrderBy] {
		orderBy = filter.OrderBy
	}
	limit := 20
	if filter.Limit > 0 && filter.Limit <= 100 {
		limit = filter.Limit
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	// count
	var total int
	countSQL := "SELECT COUNT(*) FROM task_history" + where
	if err := h.db.QueryRow(countSQL, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count history: %w", err)
	}

	// rows
	querySQL := fmt.Sprintf(
		"SELECT id, name, state, total_bots, agent_count, created_at, started_at, stopped_at, duration_sec, error_msg, starred, tags, note, config_summary FROM task_history%s ORDER BY %s LIMIT ? OFFSET ?",
		where, orderBy,
	)
	rows, err := h.db.Query(querySQL, append(args, limit, offset)...)
	if err != nil {
		return nil, fmt.Errorf("list history: %w", err)
	}
	defer rows.Close()

	var items []HistoryRecord
	for rows.Next() {
		var r HistoryRecord
		var tagsBytes, summaryBytes []byte
		var startedAt, stoppedAt sql.NullTime

		if err := rows.Scan(
			&r.ID, &r.Name, &r.State, &r.TotalBots, &r.AgentCount,
			&r.CreatedAt, &startedAt, &stoppedAt, &r.DurationSec, &r.ErrorMsg,
			&r.Starred, &tagsBytes, &r.Note, &summaryBytes,
		); err != nil {
			return nil, fmt.Errorf("scan history: %w", err)
		}

		if startedAt.Valid {
			r.StartedAt = &startedAt.Time
		}
		if stoppedAt.Valid {
			r.StoppedAt = &stoppedAt.Time
		}
		_ = json.Unmarshal(tagsBytes, &r.Tags)
		_ = json.Unmarshal(summaryBytes, &r.ConfigSummary)
		if r.Tags == nil {
			r.Tags = []string{}
		}
		items = append(items, r)
	}

	if items == nil {
		items = []HistoryRecord{}
	}
	return &HistoryListResponse{Total: total, Items: items}, nil
}

// Get 查询单条历史详情。
func (h *HistoryStore) Get(id string) (*HistoryDetail, error) {
	if h.db == nil {
		return nil, ErrHistoryNotFound
	}

	var r HistoryDetail
	var tagsBytes, summaryBytes []byte
	var startedAt, stoppedAt sql.NullTime

	err := h.db.QueryRow(`
		SELECT id, name, state, total_bots, agent_count, created_at, started_at, stopped_at,
			duration_sec, error_msg, starred, tags, note, config_summary
		FROM task_history WHERE id = ?
	`, id).Scan(
		&r.ID, &r.Name, &r.State, &r.TotalBots, &r.AgentCount,
		&r.CreatedAt, &startedAt, &stoppedAt,
		&r.DurationSec, &r.ErrorMsg, &r.Starred, &tagsBytes, &r.Note, &summaryBytes,
	)
	if err == sql.ErrNoRows {
		return nil, ErrHistoryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get history: %w", err)
	}

	if startedAt.Valid {
		r.StartedAt = &startedAt.Time
	}
	if stoppedAt.Valid {
		r.StoppedAt = &stoppedAt.Time
	}
	_ = json.Unmarshal(tagsBytes, &r.Tags)
	_ = json.Unmarshal(summaryBytes, &r.ConfigSummary)

	// assignments
	rows, err := h.db.Query(`SELECT agent_id, start_number, total_bots FROM task_assignment WHERE task_id = ?`, id)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var a Assignment
			if err := rows.Scan(&a.AgentID, &a.StartNumber, &a.TotalBots); err == nil {
				a.TaskID = id
				r.Assignments = append(r.Assignments, a)
			}
		}
	}

	// reports
	rows2, err := h.db.Query(`SELECT agent_id, agent_name, result, error_msg, finished_at, final_snapshot FROM task_report WHERE task_id = ?`, id)
	if err == nil {
		defer rows2.Close()
		for rows2.Next() {
			var rep HistoryAgentReport
			var finishedAt sql.NullTime
			var snapBytes []byte
			if err := rows2.Scan(&rep.AgentID, &rep.AgentName, &rep.Result, &rep.ErrorMsg, &finishedAt, &snapBytes); err == nil {
				if finishedAt.Valid {
					rep.FinishedAt = finishedAt.Time
				}
				_ = json.Unmarshal(snapBytes, &rep.FinalSnapshot)
				r.AgentReports = append(r.AgentReports, rep)
			}
		}
	}

	// aggregated
	var stressBytes, sysBytes []byte
	_ = h.db.QueryRow(`SELECT final_stress, final_system FROM task_aggregated WHERE task_id = ?`, id).Scan(&stressBytes, &sysBytes)
	_ = json.Unmarshal(stressBytes, &r.FinalSnapshot)
	_ = json.Unmarshal(sysBytes, &r.FinalSystem)

	return &r, nil
}

// GetConfig 获取历史配置归档。
func (h *HistoryStore) GetConfig(id string) (*TaskConfig, error) {
	if h.db == nil {
		return nil, ErrHistoryNotFound
	}

	var cfg TaskConfig
	var flowJSON, headerJSON, protoJSON, luaJSON, robotJSON []byte

	err := h.db.QueryRow(`
		SELECT flow_json, header_json, proto_files, lua_scripts, robot_config
		FROM task_config_archive WHERE task_id = ?
	`, id).Scan(&flowJSON, &headerJSON, &protoJSON, &luaJSON, &robotJSON)
	if err == sql.ErrNoRows {
		return nil, ErrHistoryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get config: %w", err)
	}

	cfg.FlowJSON = json.RawMessage(flowJSON)
	cfg.HeaderJSON = json.RawMessage(headerJSON)
	_ = json.Unmarshal(protoJSON, &cfg.ProtoFiles)
	_ = json.Unmarshal(luaJSON, &cfg.LuaScripts)
	_ = json.Unmarshal(robotJSON, &cfg.RobotConfig)

	return &cfg, nil
}

// GetTimeseries 查询时序采样数据。
func (h *HistoryStore) GetTimeseries(id string) (*TimeseriesResponse, error) {
	if h.db == nil {
		return &TimeseriesResponse{TaskID: id}, nil
	}

	rows, err := h.db.Query(`
		SELECT sampled_at, elapsed_sec, data_type, snapshot
		FROM task_timeseries WHERE task_id = ?
		ORDER BY elapsed_sec
	`, id)
	if err != nil {
		return nil, fmt.Errorf("get timeseries: %w", err)
	}
	defer rows.Close()

	resp := &TimeseriesResponse{TaskID: id}
	for rows.Next() {
		var p TimeseriesPoint
		p.TaskID = id
		if err := rows.Scan(&p.SampledAt, &p.ElapsedSec, &p.DataType, &p.Snapshot); err != nil {
			continue
		}
		switch p.DataType {
		case "stress":
			resp.Stress = append(resp.Stress, p)
		case "system":
			resp.System = append(resp.System, p)
		}
	}
	return resp, nil
}

// AllTags 返回所有去重标签。
func (h *HistoryStore) AllTags() ([]string, error) {
	if h.db == nil {
		return nil, nil
	}

	rows, err := h.db.Query(`SELECT DISTINCT jt.tag FROM task_history, JSON_TABLE(tags, '$[*]' COLUMNS(tag VARCHAR(255) PATH '$')) AS jt`)
	if err != nil {
		// fallback: scan raw JSON if JSON_TABLE not supported
		return allTagsFallback(h.db)
	}
	defer rows.Close()

	var tags []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err == nil && t != "" {
			tags = append(tags, t)
		}
	}
	return tags, nil
}

func allTagsFallback(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT tags FROM task_history WHERE tags IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	seen := make(map[string]bool)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		var tags []string
		if json.Unmarshal(raw, &tags) == nil {
			for _, t := range tags {
				seen[t] = true
			}
		}
	}

	var result []string
	for t := range seen {
		result = append(result, t)
	}
	return result, nil
}

// UpdateMeta 更新元数据（starred/tags/note）。
func (h *HistoryStore) UpdateMeta(id string, req UpdateHistoryRequest) error {
	if h.db == nil {
		return ErrHistoryNotFound
	}

	sets := []string{}
	args := []any{}

	if req.Starred != nil {
		sets = append(sets, "starred = ?")
		v := 0
		if *req.Starred {
			v = 1
		}
		args = append(args, v)
	}
	if req.Tags != nil {
		sets = append(sets, "tags = ?")
		tagsJSON, _ := json.Marshal(req.Tags)
		args = append(args, tagsJSON)
	}
	if req.Note != nil {
		sets = append(sets, "note = ?")
		args = append(args, *req.Note)
	}

	if len(sets) == 0 {
		return nil
	}

	args = append(args, id)
	result, err := h.db.Exec(
		fmt.Sprintf("UPDATE task_history SET %s WHERE id = ?", strings.Join(sets, ", ")),
		args...,
	)
	if err != nil {
		return fmt.Errorf("update meta: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrHistoryNotFound
	}
	return nil
}

// Delete 删除历史记录。starred 的需要 force=true。
func (h *HistoryStore) Delete(id string, force bool) error {
	if h.db == nil {
		return ErrHistoryNotFound
	}

	if !force {
		var starred int
		err := h.db.QueryRow(`SELECT starred FROM task_history WHERE id = ?`, id).Scan(&starred)
		if err == sql.ErrNoRows {
			return ErrHistoryNotFound
		}
		if err != nil {
			return fmt.Errorf("check starred: %w", err)
		}
		if starred == 1 {
			return ErrStarredProtected.WithMessage("task is starred, use force=true to delete")
		}
	}

	result, err := h.db.Exec(`DELETE FROM task_history WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete history: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrHistoryNotFound
	}
	return nil
}

// AppendTimeseries 追加时序采样点。
func (h *HistoryStore) AppendTimeseries(taskID string, point TimeseriesPoint) error {
	if h.db == nil {
		return nil
	}
	_, err := h.db.Exec(`
		INSERT INTO task_timeseries (task_id, sampled_at, elapsed_sec, data_type, snapshot)
		VALUES (?, ?, ?, ?, ?)
	`, taskID, point.SampledAt, point.ElapsedSec, point.DataType, point.Snapshot)
	return err
}

// PruneExpired 清理过期记录。
func (h *HistoryStore) PruneExpired(now time.Time) (int, error) {
	if h.db == nil {
		return 0, nil
	}

	cutoff := now.AddDate(0, 0, -h.cfg.RetentionDays)
	result, err := h.db.Exec(`
		DELETE FROM task_history
		WHERE starred = 0 AND stopped_at IS NOT NULL AND stopped_at < ?
	`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune expired: %w", err)
	}
	n, _ := result.RowsAffected()
	if n > 0 {
		stresslog.Info("历史清理完成",
			zap.Int64("deleted", n),
			zap.Int("retentionDays", h.cfg.RetentionDays))
	}
	return int(n), nil
}

// StartPruneLoop 启动定时清理协程。
func (h *HistoryStore) StartPruneLoop(ctx context.Context) {
	if h.db == nil {
		return
	}
	ctx, h.cancel = context.WithCancel(ctx)

	go func() {
		// 首次立即执行一次
		if _, err := h.PruneExpired(time.Now()); err != nil {
			stresslog.Error("历史清理失败", zap.Error(err))
		}

		ticker := time.NewTicker(h.prune)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := h.PruneExpired(time.Now()); err != nil {
					stresslog.Error("历史清理失败", zap.Error(err))
				}
			}
		}
	}()
}

// Close 关闭数据库连接。
func (h *HistoryStore) Close() error {
	if h.cancel != nil {
		h.cancel()
	}
	if h.db != nil {
		return h.db.Close()
	}
	return nil
}

// ── helpers ──

func buildListWhere(f HistoryFilter) (string, []any) {
	var conds []string
	var args []any

	if f.State != "" {
		conds = append(conds, "state = ?")
		args = append(args, f.State)
	}
	if !f.StartedAfter.IsZero() {
		conds = append(conds, "started_at >= ?")
		args = append(args, f.StartedAfter)
	}
	if !f.StartedBefore.IsZero() {
		conds = append(conds, "started_at < ?")
		args = append(args, f.StartedBefore)
	}
	if f.Starred != nil {
		v := 0
		if *f.Starred {
			v = 1
		}
		conds = append(conds, "starred = ?")
		args = append(args, v)
	}
	if f.Search != "" {
		conds = append(conds, "(name LIKE ? OR id LIKE ?)")
		pattern := "%" + f.Search + "%"
		args = append(args, pattern, pattern)
	}
	for _, tag := range f.Tags {
		conds = append(conds, "JSON_CONTAINS(tags, ?)")
		tagJSON, _ := json.Marshal(tag)
		args = append(args, string(tagJSON))
	}
	for _, tag := range f.TagsAll {
		conds = append(conds, "JSON_CONTAINS(tags, ?)")
		tagJSON, _ := json.Marshal(tag)
		args = append(args, string(tagJSON))
	}

	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	return where, args
}

func buildConfigSummary(task *Task) ConfigSummary {
	s := ConfigSummary{
		AuthAddr:    task.Config.RobotConfig.AuthAddr,
		Concurrency: task.Config.RobotConfig.Concurrency,
		TimeoutSec:  task.Config.RobotConfig.TimeoutSec,
	}
	if task.Config.FlowJSON != nil {
		s.FlowSizeKB = len(task.Config.FlowJSON) / 1024
	}
	s.ProtoCount = len(task.Config.ProtoFiles)
	s.ScriptCount = len(task.Config.LuaScripts)
	return s
}

func maskDSN(dsn string) string {
	idx := strings.Index(dsn, "@")
	if idx > 0 {
		return "***" + dsn[idx:]
	}
	return dsn
}
