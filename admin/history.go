package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"stressbot/monitor"
	"stressbot/robot"
	"stressbot/utils"
	json "stressbot/utils/jsonx"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// allowedOrderBy 白名单，防止 SQL 注入。
var allowedOrderBy = map[string]bool{
	"created_at DESC":   true,
	"created_at ASC":    true,
	"started_at DESC":   true,
	"started_at ASC":    true,
	"duration_sec DESC": true,
	"duration_sec ASC":  true,
	"total_bots DESC":   true,
	"total_bots ASC":    true,
	"state":             true,
	"name ASC":          true,
	"name DESC":         true,
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
		zap.String("addr", fmt.Sprintf("%s:%d", cfg.MySQL.Host, cfg.MySQL.Port)),
		zap.String("database", cfg.MySQL.Database),
		zap.Int("retentionDays", cfg.RetentionDays))

	return h, nil
}

func openDB(cfg MySQLConfig) (*sql.DB, error) {
	db, err := sql.Open("mysql", cfg.DSN())
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
	ctx := context.Background()
	for _, ddl := range allDDL {
		if _, err := h.db.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	h.dropLegacyForeignKeys(ctx)
	return nil
}

// dropLegacyForeignKeys 移除旧版本遗留的物理外键约束。
func (h *HistoryStore) dropLegacyForeignKeys(ctx context.Context) {
	type fk struct {
		table, name string
	}
	for _, k := range []fk{
		{"task_assignment", "task_assignment_ibfk_1"},
		{"task_report", "task_report_ibfk_1"},
		{"task_aggregated", "task_aggregated_ibfk_1"},
		{"task_timeseries", "task_timeseries_ibfk_1"},
		{"task_config_archive", "task_config_archive_ibfk_1"},
		{"task_agent_events", "task_agent_events_ibfk_1"},
	} {
		_, _ = h.db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s DROP FOREIGN KEY %s", k.table, k.name))
	}
}

// Archive 将终态任务归档到 MySQL（事务写入 6 张表）。
func (h *HistoryStore) Archive(ctx context.Context, task *Task, finalStress *monitor.CollectorSnapshot, finalSys ClusterSystemSnapshot) error {
	if h.db == nil {
		return nil
	}

	tx, err := h.db.BeginTx(ctx, nil)
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
	activeAgentCount := len(task.SucceededAgents)
	stageCount := 0
	if task.Config.RobotConfig.RampUp != nil {
		stageCount = len(task.Config.RobotConfig.RampUp.Stages)
	}
	summaryJSON, _ := json.Marshal(buildConfigSummary(task)) // []string/struct 序列化不会失败

	// 收藏/标签/备注不在归档时写入，统一由 task_meta 懒创建（见 UpsertMeta）。
	_, err = tx.Exec(`
		INSERT INTO task_history (id, name, state, total_bots, agent_count, active_agent_count,
			created_at, started_at, stopped_at, duration_sec, error_msg,
			debug_mode, config_summary, stage_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			state=VALUES(state), stopped_at=VALUES(stopped_at),
			duration_sec=VALUES(duration_sec), error_msg=VALUES(error_msg),
			stage_count=VALUES(stage_count),
			active_agent_count=VALUES(active_agent_count)
	`,
		task.ID, task.Name, string(task.State), task.TotalBots, agentCount, activeAgentCount,
		task.CreatedAt, task.StartedAt, task.StoppedAt, durationSec, task.ErrorMsg,
		task.Config.RobotConfig.DebugMode, summaryJSON, stageCount,
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
	// 构建节点名称映射，用于填充 task_report 和 task_agent_events 的 agent_name
	agentNames := make(map[string]string, len(task.Assignments))
	for _, a := range task.Assignments {
		agentNames[a.AgentID] = a.AgentName
	}
	// 阶段段落计划：用于把 Agent 上报的「配置阶段下标」映射为连续 1-based 段落号。
	plan := buildStagePlan(task.Config.RobotConfig.RampUp)

	// 3a. 整体最终报告：stage_index = -1（老入口不变）。
	// 注意：有 reset 任务中，Agent 每段 reset 采集器，故最终报告仅覆盖最后一个段落。
	for agentID, report := range task.Reports {
		if err := insertTaskReport(tx, task.ID, agentID, agentNames[agentID], report, -1); err != nil {
			return fmt.Errorf("insert task_report: %w", err)
		}
	}
	// 3b. reset 边界阶段段落报告：映射为连续段落号写入。
	for _, report := range task.StageReports {
		segNo := plan.SegmentNoForResetIndex(report.StageIndex)
		if segNo == 0 {
			continue // 非计划内（异常配置），跳过
		}
		if err := insertTaskReport(tx, task.ID, report.AgentID, agentNames[report.AgentID], report, segNo); err != nil {
			return fmt.Errorf("insert task_report (stage): %w", err)
		}
	}
	// 3c. 有 reset：最终报告额外写入最后一个段落，使末段详情可像普通详情查看节点报告。
	if plan.HasReset {
		finalSeg := plan.FinalSegmentNo()
		for agentID, report := range task.Reports {
			if err := insertTaskReport(tx, task.ID, agentID, agentNames[agentID], report, finalSeg); err != nil {
				return fmt.Errorf("insert task_report (final stage): %w", err)
			}
		}
	}

	// 4. task_aggregated
	// 4a. 整体最终聚合：stage_index = -1（老入口不变）。
	stressJSON, _ := json.Marshal(finalStress) // 同上
	sysJSON, _ := json.Marshal(finalSys)       // 同上
	_, err = tx.Exec(`
		INSERT INTO task_aggregated (task_id, stage_index, final_stress, final_system)
		VALUES (?, -1, ?, ?)
		ON DUPLICATE KEY UPDATE final_stress=VALUES(final_stress), final_system=VALUES(final_system)
	`, task.ID, stressJSON, sysJSON)
	if err != nil {
		return fmt.Errorf("insert task_aggregated: %w", err)
	}
	// 4b. 有 reset：写入各阶段段落聚合。
	if plan.HasReset {
		// 中间段落：按段号分组 StageReports 的 FinalSnapshot 聚合（段级系统快照暂缺）。
		segSnaps := map[int][]*monitor.CollectorSnapshot{}
		for _, report := range task.StageReports {
			segNo := plan.SegmentNoForResetIndex(report.StageIndex)
			if segNo == 0 || report.FinalSnapshot == nil {
				continue
			}
			segSnaps[segNo] = append(segSnaps[segNo], report.FinalSnapshot)
		}
		emptySysJSON, _ := json.Marshal(ClusterSystemSnapshot{})
		for segNo, snaps := range segSnaps {
			merged := monitor.MergeSnapshots(snaps)
			mergedJSON, _ := json.Marshal(merged)
			if err := insertTaskAggregated(tx, task.ID, segNo, mergedJSON, emptySysJSON); err != nil {
				return fmt.Errorf("insert task_aggregated (stage): %w", err)
			}
		}
		// 最终段落：等于整体最终聚合（含系统终态）。
		if err := insertTaskAggregated(tx, task.ID, plan.FinalSegmentNo(), stressJSON, sysJSON); err != nil {
			return fmt.Errorf("insert task_aggregated (final stage): %w", err)
		}
	}

	// 5. task_config_archive
	flowJSON := task.Config.FlowJSON
	protoFilesJSON, _ := json.Marshal(task.Config.ProtoFiles) // 同上
	luaScriptsJSON, _ := json.Marshal(task.Config.LuaScripts) // 同上
	robotCfgJSON, _ := json.Marshal(task.Config.RobotConfig)  // 同上
	_, err = tx.Exec(`
		INSERT INTO task_config_archive (task_id, flow_json, proto_files, lua_scripts, robot_config)
		VALUES (?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE flow_json=VALUES(flow_json)
	`, task.ID, flowJSON, protoFilesJSON, luaScriptsJSON, robotCfgJSON)
	if err != nil {
		return fmt.Errorf("insert task_config_archive: %w", err)
	}

	// 6. task_agent_events
	for _, evt := range task.AgentEvents {
		_, err = tx.Exec(`
			INSERT INTO task_agent_events (task_id, agent_id, agent_name, event_type, timestamp, detail)
			VALUES (?, ?, ?, ?, ?, ?)
		`, task.ID, evt.AgentID, evt.AgentName, evt.Type, evt.Timestamp, evt.Detail)
		if err != nil {
			return fmt.Errorf("insert task_agent_events: %w", err)
		}
	}

	return tx.Commit()
}

// List 分页查询历史记录。
func (h *HistoryStore) List(ctx context.Context, filter HistoryFilter) (*HistoryListResponse, error) {
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

	const fromJoin = " FROM task_history th LEFT JOIN task_meta m ON m.task_id = th.id AND m.stage_index = -1"

	var total int
	countSQL := "SELECT COUNT(*)" + fromJoin + where
	if err := h.db.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count history: %w", err)
	}

	querySQL := fmt.Sprintf(
		"SELECT th.id, th.name, th.state, th.total_bots, th.agent_count, th.active_agent_count, th.created_at, th.started_at, th.stopped_at, th.duration_sec, th.error_msg, COALESCE(m.starred, 0), m.tags, COALESCE(m.note, ''), th.debug_mode, th.config_summary, th.stage_count%s%s ORDER BY %s LIMIT ? OFFSET ?",
		fromJoin, where, orderBy,
	)
	rows, err := h.db.QueryContext(ctx, querySQL, append(args, limit, offset)...)
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
			&r.ID, &r.Name, &r.State, &r.TotalBots, &r.AgentCount, &r.ActiveAgentCount,
			&r.CreatedAt, &startedAt, &stoppedAt, &r.DurationSec, &r.ErrorMsg,
			&r.Starred, &tagsBytes, &r.Note, &r.DebugMode, &summaryBytes, &r.StageCount,
		); err != nil {
			return nil, fmt.Errorf("scan history: %w", err)
		}

		if startedAt.Valid {
			r.StartedAt = &startedAt.Time
		}
		if stoppedAt.Valid {
			r.StoppedAt = &stoppedAt.Time
		}
		_ = json.Unmarshal(tagsBytes, &r.Tags)             // DB 字段可选，缺失时零值可用
		_ = json.Unmarshal(summaryBytes, &r.ConfigSummary) // 同上
		if r.Tags == nil {
			r.Tags = []string{}
		}
		items = append(items, r)
	}

	if items == nil {
		items = []HistoryRecord{}
	}
	h.enrichStageGroups(ctx, items, filter.IncludeStages)
	return &HistoryListResponse{Total: total, Items: items}, nil
}

// enrichStageGroups 为含 reset 的渐进式加压父记录标记 HasResetStages，
// 并在 includeStages 时填充阶段段落子记录（虚拟记录，不落库）。
// 批量读取本页 stage_count>0 行的 robot_config，避免 N+1。
func (h *HistoryStore) enrichStageGroups(ctx context.Context, items []HistoryRecord, includeStages bool) {
	ids := make([]string, 0)
	for i := range items {
		if items[i].StageCount > 0 {
			ids = append(ids, items[i].ID)
		}
	}
	if len(ids) == 0 {
		return
	}

	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := h.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT task_id, robot_config FROM task_config_archive WHERE task_id IN (%s)`, placeholders),
		args...)
	if err != nil {
		stresslog.Warn("[ADMIN] 阶段组展开读取配置失败", zap.Error(err))
		return
	}
	defer rows.Close()

	plans := make(map[string]StagePlan, len(ids))
	for rows.Next() {
		var tid string
		var robotJSON []byte
		if err := rows.Scan(&tid, &robotJSON); err != nil {
			continue
		}
		var robotCfg RobotConfig
		_ = json.Unmarshal(robotJSON, &robotCfg)
		plans[tid] = buildStagePlan(robotCfg.RampUp)
	}

	var metaByTask map[string]map[int]metaRecord
	if includeStages {
		metaByTask = h.loadStageMetaBatch(ctx, ids)
	}

	for i := range items {
		plan, ok := plans[items[i].ID]
		if !ok || !plan.HasReset {
			continue
		}
		items[i].HasResetStages = true
		if !includeStages {
			continue
		}
		metaByStage := metaByTask[items[i].ID]
		children := make([]HistoryRecord, 0, len(plan.Segments))
		for _, seg := range plan.Segments {
			child := HistoryRecord{
				ID:         items[i].ID,
				Name:       items[i].Name,
				State:      items[i].State,
				RecordKind: "stage",
				ParentID:   items[i].ID,
				StageIndex: seg.No,
				StageLabel: seg.Label(),
				StageFrom:  seg.FromStage,
				StageTo:    seg.ToStage,
				TotalBots:  seg.PeakBots,
			}
			if m, ok := metaByStage[seg.No]; ok {
				child.Starred = m.Starred
				child.Tags = m.Tags
				child.Note = m.Note
			}
			children = append(children, child)
		}
		items[i].Children = children
	}
}

// getHistoryRecord 查询历史任务基础记录。
func (h *HistoryStore) getHistoryRecord(ctx context.Context, id string) (*HistoryRecord, error) {
	if h.db == nil {
		return nil, ErrHistoryNotFound
	}

	var r HistoryRecord
	var tagsBytes, summaryBytes []byte
	var startedAt, stoppedAt sql.NullTime

	err := h.db.QueryRowContext(ctx, `
		SELECT th.id, th.name, th.state, th.total_bots, th.agent_count, th.active_agent_count,
			th.created_at, th.started_at, th.stopped_at,
			th.duration_sec, th.error_msg,
			COALESCE(m.starred, 0), m.tags, COALESCE(m.note, ''), th.debug_mode, th.config_summary, th.stage_count
		FROM task_history th
		LEFT JOIN task_meta m ON m.task_id = th.id AND m.stage_index = -1
		WHERE th.id = ?
	`, id).Scan(
		&r.ID, &r.Name, &r.State, &r.TotalBots, &r.AgentCount, &r.ActiveAgentCount,
		&r.CreatedAt, &startedAt, &stoppedAt,
		&r.DurationSec, &r.ErrorMsg, &r.Starred, &tagsBytes, &r.Note, &r.DebugMode, &summaryBytes, &r.StageCount,
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
	return &r, nil
}

// Get 查询单条完整历史归档。
func (h *HistoryStore) Get(ctx context.Context, id string) (*HistoryDetail, error) {
	record, err := h.getHistoryRecord(ctx, id)
	if err != nil {
		return nil, err
	}

	r := HistoryDetail{HistoryRecord: *record}
	r.Assignments, _ = h.queryAssignments(ctx, id)
	r.AgentReports, _ = h.queryReports(ctx, id)
	h.queryAggregated(ctx, id, &r)
	r.AgentEvents, _ = h.queryAgentEvents(ctx, id)

	return &r, nil
}

// GetDetailSummary 查询历史详情页展示数据。
// stageIndex > 0 时返回该阶段段落详情；<=0 返回整体/最终详情。
func (h *HistoryStore) GetDetailSummary(ctx context.Context, id string, stageIndex int) (*HistoryDetailResponse, error) {
	record, err := h.getHistoryRecord(ctx, id)
	if err != nil {
		return nil, err
	}

	queryStage := -1
	if stageIndex > 0 {
		queryStage = stageIndex
		h.decorateStageRecord(ctx, id, record, stageIndex)
		// 段落级元数据覆盖任务级（收藏/标签/备注分属各段落）
		if m, ok := h.loadMeta(ctx, id, stageIndex); ok {
			record.Starred = m.Starred
			record.Tags = m.Tags
			record.Note = m.Note
		} else {
			record.Starred = false
			record.Tags = nil
			record.Note = ""
		}
	}

	reports, _ := h.queryReportSummaries(ctx, id, queryStage)
	stress, system := h.queryAggregatedSummary(ctx, id, queryStage)
	events, _ := h.queryAgentEvents(ctx, id)

	return &HistoryDetailResponse{
		HistoryRecord: *record,
		AgentReports:  reports,
		AgentEvents:   events,
		FinalSnapshot: stress,
		FinalSystem:   system,
	}, nil
}

// decorateStageRecord 为阶段段落详情填充展示字段（段号、标签、范围）。
func (h *HistoryStore) decorateStageRecord(ctx context.Context, id string, record *HistoryRecord, stageIndex int) {
	plan, err := h.stagePlanForTask(ctx, id)
	if err != nil || !plan.HasReset {
		return
	}
	record.RecordKind = "stage"
	record.ParentID = id
	record.StageIndex = stageIndex
	for _, seg := range plan.Segments {
		if seg.No == stageIndex {
			record.StageLabel = seg.Label()
			record.StageFrom = seg.FromStage
			record.StageTo = seg.ToStage
			record.TotalBots = seg.PeakBots
			break
		}
	}
}

// stagePlanForTask 读取归档的 RobotConfig 并推导阶段段落计划。
func (h *HistoryStore) stagePlanForTask(ctx context.Context, id string) (StagePlan, error) {
	var robotJSON []byte
	err := h.db.QueryRowContext(ctx, `SELECT robot_config FROM task_config_archive WHERE task_id = ?`, id).Scan(&robotJSON)
	if err != nil {
		return StagePlan{}, err
	}
	var robotCfg RobotConfig
	_ = json.Unmarshal(robotJSON, &robotCfg)
	return buildStagePlan(robotCfg.RampUp), nil
}

// queryAssignments 查询任务分配记录。
func (h *HistoryStore) queryAssignments(ctx context.Context, taskID string) ([]Assignment, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT agent_id, start_number, total_bots FROM task_assignment WHERE task_id = ?`, taskID)
	if err != nil {
		return nil, fmt.Errorf("query assignments: %w", err)
	}
	defer rows.Close()

	var items []Assignment
	for rows.Next() {
		var a Assignment
		if err := rows.Scan(&a.AgentID, &a.StartNumber, &a.TotalBots); err != nil {
			return nil, fmt.Errorf("scan assignment: %w", err)
		}
		a.TaskID = taskID
		items = append(items, a)
	}
	return items, nil
}

// queryReports 查询 Agent 上报结果。
func (h *HistoryStore) queryReports(ctx context.Context, taskID string) ([]HistoryAgentReport, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT agent_id, agent_name, result, error_msg, finished_at, final_snapshot, cleanup_status FROM task_report WHERE task_id = ?`, taskID)
	if err != nil {
		return nil, fmt.Errorf("query reports: %w", err)
	}
	defer rows.Close()

	var items []HistoryAgentReport
	for rows.Next() {
		var rep HistoryAgentReport
		var finishedAt sql.NullTime
		var snapBytes []byte
		var cleanupBytes []byte
		if err := rows.Scan(&rep.AgentID, &rep.AgentName, &rep.Result, &rep.ErrorMsg, &finishedAt, &snapBytes, &cleanupBytes); err != nil {
			return nil, fmt.Errorf("scan report: %w", err)
		}
		if finishedAt.Valid {
			rep.FinishedAt = finishedAt.Time
		}
		_ = json.Unmarshal(snapBytes, &rep.FinalSnapshot) // 同上
		if len(cleanupBytes) > 0 {
			var cleanup robot.CleanupStatus
			if err := json.Unmarshal(cleanupBytes, &cleanup); err == nil && cleanup.Status != "" {
				rep.CleanupStatus = &cleanup
			}
		}
		items = append(items, rep)
	}
	return items, nil
}

// queryReportSummaries 查询历史详情页需要的节点结果摘要。
// stageIndex <=0 时查整体（-1）；>0 时查对应阶段段落。
func (h *HistoryStore) queryReportSummaries(ctx context.Context, taskID string, stageIndex int) ([]HistoryAgentReportSummary, error) {
	if stageIndex <= 0 {
		stageIndex = -1
	}
	rows, err := h.db.QueryContext(ctx, `
		SELECT agent_id, agent_name, result, error_msg, finished_at, cleanup_status
		FROM task_report
		WHERE task_id = ? AND stage_index = ?
	`, taskID, stageIndex)
	if err != nil {
		return nil, fmt.Errorf("query report summaries: %w", err)
	}
	defer rows.Close()

	items := []HistoryAgentReportSummary{}
	for rows.Next() {
		var rep HistoryAgentReportSummary
		var finishedAt sql.NullTime
		var cleanupBytes []byte
		if err := rows.Scan(&rep.AgentID, &rep.AgentName, &rep.Result, &rep.ErrorMsg, &finishedAt, &cleanupBytes); err != nil {
			return nil, fmt.Errorf("scan report summary: %w", err)
		}
		if finishedAt.Valid {
			rep.FinishedAt = finishedAt.Time
		}
		if len(cleanupBytes) > 0 {
			var cleanup robot.CleanupStatus
			if err := json.Unmarshal(cleanupBytes, &cleanup); err == nil && cleanup.Status != "" {
				rep.CleanupStatus = &cleanup
			}
		}
		items = append(items, rep)
	}
	return items, nil
}

// queryAggregated 查询聚合指标，填入 HistoryDetail。
func (h *HistoryStore) queryAggregated(ctx context.Context, taskID string, r *HistoryDetail) {
	var stressBytes, sysBytes []byte
	err := h.db.QueryRowContext(ctx, `SELECT final_stress, final_system FROM task_aggregated WHERE task_id = ? AND stage_index = -1`, taskID).Scan(&stressBytes, &sysBytes)
	if err != nil && err != sql.ErrNoRows {
		stresslog.Warn("[ADMIN] 查询聚合指标失败", zap.String("taskID", taskID), zap.Error(err))
		return
	}
	_ = json.Unmarshal(stressBytes, &r.FinalSnapshot) // 同上
	_ = json.Unmarshal(sysBytes, &r.FinalSystem)      // 同上
}

// queryAggregatedSummary 查询历史详情页需要的聚合指标摘要。
// stageIndex <=0 时查整体（-1）；>0 时查对应阶段段落。
func (h *HistoryStore) queryAggregatedSummary(ctx context.Context, taskID string, stageIndex int) (HistoryStressSnapshotSummary, HistorySystemSummary) {
	if stageIndex <= 0 {
		stageIndex = -1
	}
	var stressBytes, sysBytes []byte
	err := h.db.QueryRowContext(ctx, `SELECT final_stress, final_system FROM task_aggregated WHERE task_id = ? AND stage_index = ?`, taskID, stageIndex).Scan(&stressBytes, &sysBytes)
	if err != nil && err != sql.ErrNoRows {
		stresslog.Warn("[ADMIN] 查询聚合指标摘要失败", zap.String("taskID", taskID), zap.Error(err))
		return projectStressSnapshot(monitor.CollectorSnapshot{}), HistorySystemSummary{}
	}

	var stress monitor.CollectorSnapshot
	var system ClusterSystemSnapshot
	_ = json.Unmarshal(stressBytes, &stress)
	_ = json.Unmarshal(sysBytes, &system)
	return projectStressSnapshot(stress), projectSystemSnapshot(system)
}

// queryAgentEvents 查询 Agent 事件。
func (h *HistoryStore) queryAgentEvents(ctx context.Context, taskID string) ([]AgentEvent, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT agent_id, agent_name, event_type, timestamp, detail FROM task_agent_events WHERE task_id = ? ORDER BY timestamp`, taskID)
	if err != nil {
		return nil, fmt.Errorf("query agent events: %w", err)
	}
	defer rows.Close()

	var items []AgentEvent
	for rows.Next() {
		var evt AgentEvent
		var detail sql.NullString
		if err := rows.Scan(&evt.AgentID, &evt.AgentName, &evt.Type, &evt.Timestamp, &detail); err != nil {
			return nil, fmt.Errorf("scan agent event: %w", err)
		}
		if detail.Valid {
			evt.Detail = detail.String
		}
		items = append(items, evt)
	}
	return items, nil
}

// GetCompareTask 查询历史对比页需要的任务指标。
// stageIndex > 0 时对比该阶段段落；<=0 对比整体/最终。
func (h *HistoryStore) GetCompareTask(ctx context.Context, id string, stageIndex int) (*HistoryCompareTask, error) {
	record, err := h.getHistoryRecord(ctx, id)
	if err != nil {
		return nil, err
	}

	queryStage := -1
	if stageIndex > 0 {
		queryStage = stageIndex
	}
	var stressBytes []byte
	err = h.db.QueryRowContext(ctx, `SELECT final_stress FROM task_aggregated WHERE task_id = ? AND stage_index = ?`, id, queryStage).Scan(&stressBytes)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("get compare stress: %w", err)
	}

	var stress monitor.CollectorSnapshot
	_ = json.Unmarshal(stressBytes, &stress)
	actions := make([]HistoryCompareAction, 0, len(stress.Actions))
	for _, a := range stress.Actions {
		actions = append(actions, HistoryCompareAction{
			Name:                     a.Name,
			SampleCount:              a.SampleCount,
			RTTApdex:                 a.RTTApdex,
			TotalDurationApdex:       a.TotalDurationApdex,
			RTT:                      projectHistogram(a.RTT),
			TotalDuration:            projectHistogram(a.TotalDuration),
			TotalDurationSampleCount: a.TotalDurationSampleCount,
		})
	}

	task := &HistoryCompareTask{
		ID:          record.ID,
		Name:        record.Name,
		StartedAt:   record.StartedAt,
		DurationSec: record.DurationSec,
		TotalBots:   record.TotalBots,
		StageIndex:  -1,
		FinalSnapshot: HistoryCompareSnapshot{
			TotalActions: stress.TotalActions,
			Actions:      actions,
		},
	}
	if stageIndex > 0 {
		task.ParentID = id
		task.StageIndex = stageIndex
		if plan, err := h.stagePlanForTask(ctx, id); err == nil && plan.HasReset {
			for _, seg := range plan.Segments {
				if seg.No == stageIndex {
					task.StageLabel = seg.Label()
					task.TotalBots = seg.PeakBots
					break
				}
			}
		}
	}
	return task, nil
}

// GetConfig 获取历史配置归档。
func (h *HistoryStore) GetConfig(ctx context.Context, id string) (*TaskConfig, error) {
	if h.db == nil {
		return nil, ErrHistoryNotFound
	}

	var cfg TaskConfig
	var flowJSON, protoJSON, luaJSON, robotJSON []byte

	err := h.db.QueryRowContext(ctx, `
		SELECT flow_json, proto_files, lua_scripts, robot_config
		FROM task_config_archive WHERE task_id = ?
	`, id).Scan(&flowJSON, &protoJSON, &luaJSON, &robotJSON)
	if err == sql.ErrNoRows {
		return nil, ErrHistoryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get config: %w", err)
	}

	cfg.FlowJSON = json.RawMessage(flowJSON)
	_ = json.Unmarshal(protoJSON, &cfg.ProtoFiles)  // 同上
	_ = json.Unmarshal(luaJSON, &cfg.LuaScripts)    // 同上
	_ = json.Unmarshal(robotJSON, &cfg.RobotConfig) // 同上

	return &cfg, nil
}

// GetConfigSummary 获取历史配置摘要。
func (h *HistoryStore) GetConfigSummary(ctx context.Context, id string) (*HistoryConfigSummaryResponse, error) {
	record, err := h.getHistoryRecord(ctx, id)
	if err != nil {
		return nil, err
	}

	var robotJSON []byte
	err = h.db.QueryRowContext(ctx, `SELECT robot_config FROM task_config_archive WHERE task_id = ?`, id).Scan(&robotJSON)
	if err == sql.ErrNoRows {
		return nil, ErrHistoryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get config summary: %w", err)
	}

	var robotCfg RobotConfig
	_ = json.Unmarshal(robotJSON, &robotCfg)
	return &HistoryConfigSummaryResponse{
		TaskID:      id,
		Name:        record.Name,
		TotalBots:   record.TotalBots,
		RobotConfig: robotCfg,
	}, nil
}

// GetConfigArchive 获取历史完整配置归档响应。
func (h *HistoryStore) GetConfigArchive(ctx context.Context, id string) (*HistoryConfigArchiveResponse, error) {
	record, err := h.getHistoryRecord(ctx, id)
	if err != nil {
		return nil, err
	}
	cfg, err := h.GetConfig(ctx, id)
	if err != nil {
		return nil, err
	}

	scripts := make(map[string]string, len(cfg.LuaScripts))
	for k, v := range cfg.LuaScripts {
		scripts[k] = string(v)
	}
	protoFiles := make(map[string]string, len(cfg.ProtoFiles))
	for k, v := range cfg.ProtoFiles {
		protoFiles[k] = string(v)
	}

	return &HistoryConfigArchiveResponse{
		TaskID:      id,
		Name:        record.Name,
		TotalBots:   record.TotalBots,
		RobotConfig: cfg.RobotConfig,
		FlowJSON:    cfg.FlowJSON,
		ProtoFiles:  protoFiles,
		Scripts:     scripts,
	}, nil
}

const (
	defaultHistoryTimeseriesMaxPoints = 600
	maxHistoryTimeseriesMaxPoints     = 2000
)

// GetTimeseries 查询时序采样数据。
// stageIndex > 0 时只返回该阶段段落的采样点；<=0 返回全部点（整体趋势）。
func (h *HistoryStore) GetTimeseries(ctx context.Context, id string, maxPoints, stageIndex int) (*TimeseriesResponse, error) {
	if h.db == nil {
		return &TimeseriesResponse{TaskID: id}, nil
	}
	maxPoints = normalizeTimeseriesMaxPoints(maxPoints)

	query := `
		SELECT sampled_at, elapsed_sec, stage_index, total_qps, rtt_apdex, total_duration_apdex,
			rtt_avg_ms, rtt_p95_ms, rtt_p99_ms,
			total_duration_avg_ms, total_duration_p95_ms, total_duration_p99_ms,
			client_avg_ms, encode_avg_ms, decode_avg_ms,
			bots_running, bots_errored, send_kbps, recv_kbps,
			avg_cpu_percent, max_cpu_percent, avg_mem_percent, max_mem_percent,
			goroutines, threads, fds, online_count, offline_count
		FROM task_timeseries WHERE task_id = ?`
	args := []any{id}
	if stageIndex > 0 {
		query += ` AND stage_index = ?`
		args = append(args, stageIndex)
	}
	query += ` ORDER BY elapsed_sec`

	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get timeseries: %w", err)
	}
	defer rows.Close()

	points := []HistoryTrendPointResponse{}
	for rows.Next() {
		var p HistoryTrendPointResponse
		var rttApdex, totalDurationApdex sql.NullFloat64
		if err := rows.Scan(
			&p.SampledAt, &p.ElapsedSec, &p.StageIndex, &p.TotalQPS, &rttApdex, &totalDurationApdex,
			&p.RTTAvgMs, &p.RTTP95Ms, &p.RTTP99Ms,
			&p.TotalDurationAvgMs, &p.TotalDurationP95Ms, &p.TotalDurationP99Ms,
			&p.ClientAvgMs, &p.EncodeAvgMs, &p.DecodeAvgMs,
			&p.BotsRunning, &p.BotsErrored, &p.SendKBps, &p.RecvKBps,
			&p.AvgCPUPercent, &p.MaxCPUPercent, &p.AvgMemPercent, &p.MaxMemPercent,
			&p.Goroutines, &p.Threads, &p.FDs, &p.OnlineCount, &p.OfflineCount,
		); err != nil {
			continue
		}
		if rttApdex.Valid {
			p.RTTApdex = &rttApdex.Float64
		}
		if totalDurationApdex.Valid {
			p.TotalDurationApdex = &totalDurationApdex.Float64
		}
		points = append(points, p)
	}

	originalCount := len(points)
	resp := &TimeseriesResponse{
		TaskID:        id,
		Points:        sampleHistoryTrendPoints(points, maxPoints),
		OriginalCount: originalCount,
		MaxPoints:     maxPoints,
	}
	resp.Sampled = len(resp.Points) < originalCount
	return resp, nil
}

func normalizeTimeseriesMaxPoints(maxPoints int) int {
	if maxPoints <= 0 {
		return defaultHistoryTimeseriesMaxPoints
	}
	if maxPoints > maxHistoryTimeseriesMaxPoints {
		return maxHistoryTimeseriesMaxPoints
	}
	return maxPoints
}

func sampleHistoryTrendPoints(points []HistoryTrendPointResponse, maxPoints int) []HistoryTrendPointResponse {
	if len(points) <= maxPoints {
		return points
	}
	if maxPoints <= 1 {
		return points[len(points)-1:]
	}

	result := make([]HistoryTrendPointResponse, 0, maxPoints)
	lastIdx := -1
	for i := 0; i < maxPoints; i++ {
		idx := int(float64(i) * float64(len(points)-1) / float64(maxPoints-1))
		if idx == lastIdx {
			continue
		}
		result = append(result, points[idx])
		lastIdx = idx
	}
	return result
}

func projectStressSnapshot(s monitor.CollectorSnapshot) HistoryStressSnapshotSummary {
	actions := make([]HistoryActionSummary, 0, len(s.Actions))
	for _, a := range s.Actions {
		actions = append(actions, projectActionSnapshot(a))
	}
	return HistoryStressSnapshotSummary{
		Timestamp:    s.Timestamp,
		UptimeSec:    s.UptimeSec,
		TotalActions: s.TotalActions,
		ApdexT:       s.ApdexT,
		Robots:       s.Robots,
		Connections:  s.Connections,
		Bandwidth:    s.Bandwidth,
		Actions:      actions,
	}
}

func projectActionSnapshot(a monitor.ActionSnapshot) HistoryActionSummary {
	return HistoryActionSummary{
		Name:                     a.Name,
		SampleCount:              a.SampleCount,
		SuccessCount:             a.SuccessCount,
		FailureCount:             a.FailureCount,
		TimeoutCount:             a.TimeoutCount,
		CanceledCount:            a.CanceledCount,
		Executing:                a.Executing,
		SuccessRate:              a.SuccessRate,
		AvgSendBytes:             a.AvgSendBytes,
		AvgRecvBytes:             a.AvgRecvBytes,
		RTTApdex:                 a.RTTApdex,
		TotalDurationApdex:       a.TotalDurationApdex,
		RTT:                      projectHistogram(a.RTT),
		TotalDuration:            projectHistogram(a.TotalDuration),
		ClientAvgMs:              a.ClientAvgMs,
		EncodeAvgMs:              a.EncodeAvgMs,
		DecodeAvgMs:              a.DecodeAvgMs,
		ParseStoreAvgMs:          a.ParseStoreAvgMs,
		RTTSampleCount:           a.RTTSampleCount,
		TotalDurationSampleCount: a.TotalDurationSampleCount,
		AvgQPS:                   a.AvgQPS,
		Errors:                   a.Errors,
	}
}

func projectHistogram(h monitor.HistogramSnapshot) HistoryHistogramSummary {
	return HistoryHistogramSummary{
		MaxMs: h.MaxMs,
		AvgMs: h.AvgMs,
		P50Ms: h.P50Ms,
		P95Ms: h.P95Ms,
		P99Ms: h.P99Ms,
	}
}

func projectSystemSnapshot(s ClusterSystemSnapshot) HistorySystemSummary {
	return HistorySystemSummary{
		AvgCPUPercent:    s.AvgCPUPercent,
		MaxCPUPercent:    s.MaxCPUPercent,
		HotAgentName:     s.HotAgentName,
		AvgMemPercent:    s.AvgMemPercent,
		MaxMemPercent:    s.MaxMemPercent,
		HotMemAgentName:  s.HotMemAgentName,
		TotalMemMB:       s.TotalMemMB,
		UsedMemMB:        s.UsedMemMB,
		TotalNetSendKBps: s.TotalNetSendKBps,
		TotalNetRecvKBps: s.TotalNetRecvKBps,
		TotalGoroutines:  s.TotalGoroutines,
		TotalThreads:     s.TotalThreads,
		TotalFDs:         s.TotalFDs,
	}
}

// AllTags 返回所有去重标签。
func (h *HistoryStore) AllTags(ctx context.Context) ([]string, error) {
	if h.db == nil {
		return nil, nil
	}

	rows, err := h.db.QueryContext(ctx, `SELECT DISTINCT jt.tag FROM task_meta, JSON_TABLE(tags, '$[*]' COLUMNS(tag VARCHAR(255) PATH '$')) AS jt`)
	if err != nil {
		return allTagsFallback(ctx, h.db)
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

func allTagsFallback(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT tags FROM task_meta WHERE tags IS NOT NULL`)
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

// UpsertMeta 更新元数据（starred/tags/note）。stageIndex<=0 归一化为 -1（任务级，
// 所有任务都用）；stageIndex>=1 为 reset 渐进式加压的段落级。所有元数据统一存于 task_meta，
// 按 (task_id, stage_index) upsert，与 task_report/aggregated/timeseries 的键约定一致。
func (h *HistoryStore) UpsertMeta(ctx context.Context, id string, stageIndex int, req UpdateHistoryRequest) error {
	if h.db == nil {
		return ErrHistoryNotFound
	}
	if stageIndex <= 0 {
		stageIndex = -1
	}
	if err := h.db.QueryRowContext(ctx, `SELECT id FROM task_history WHERE id = ?`, id).Scan(new(string)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrHistoryNotFound
		}
		return fmt.Errorf("check history exists: %w", err)
	}

	// 读出当前值后按请求里出现的字段合并，未出现的字段保持不变（部分更新）。
	cur, _ := h.loadMeta(ctx, id, stageIndex)
	if req.Starred != nil {
		cur.Starred = *req.Starred
	}
	if req.Tags != nil {
		cur.Tags = *req.Tags
	}
	if req.Note != nil {
		cur.Note = *req.Note
	}

	starredVal := 0
	if cur.Starred {
		starredVal = 1
	}
	tagsJSON, _ := json.Marshal(cur.Tags)
	_, err := h.db.ExecContext(ctx, `
		INSERT INTO task_meta (task_id, stage_index, starred, tags, note, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE starred = VALUES(starred), tags = VALUES(tags), note = VALUES(note), updated_at = VALUES(updated_at)
	`, id, stageIndex, starredVal, tagsJSON, cur.Note, time.Now())
	if err != nil {
		return fmt.Errorf("upsert meta: %w", err)
	}
	return nil
}

// metaRecord 是一份元数据的内存表示（任务级或段落级同构）。
type metaRecord struct {
	Starred bool
	Tags    []string
	Note    string
}

// loadMeta 读取单份元数据（task_id, stageIndex）；不存在时返回零值 + false。
func (h *HistoryStore) loadMeta(ctx context.Context, id string, stageIndex int) (metaRecord, bool) {
	var m metaRecord
	var starred int
	var tagsBytes []byte
	err := h.db.QueryRowContext(ctx,
		`SELECT starred, tags, note FROM task_meta WHERE task_id = ? AND stage_index = ?`,
		id, stageIndex).Scan(&starred, &tagsBytes, &m.Note)
	if err != nil {
		return m, false
	}
	m.Starred = starred == 1
	_ = json.Unmarshal(tagsBytes, &m.Tags)
	return m, true
}

// loadStageMetaBatch 批量读取多个任务的段落级元数据（stage_index>=1），按 task_id → (stage_index → meta) 组织。
// 任务级（-1）元数据由列表 JOIN 直接带出，此处只关心段落行。
func (h *HistoryStore) loadStageMetaBatch(ctx context.Context, ids []string) map[string]map[int]metaRecord {
	out := make(map[string]map[int]metaRecord, len(ids))
	if len(ids) == 0 {
		return out
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := h.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT task_id, stage_index, starred, tags, note FROM task_meta WHERE stage_index >= 1 AND task_id IN (%s)`, placeholders),
		args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var tid string
		var idx, starred int
		var tagsBytes []byte
		var note string
		if err := rows.Scan(&tid, &idx, &starred, &tagsBytes, &note); err != nil {
			continue
		}
		m := metaRecord{Starred: starred == 1, Note: note}
		_ = json.Unmarshal(tagsBytes, &m.Tags)
		if out[tid] == nil {
			out[tid] = make(map[int]metaRecord)
		}
		out[tid][idx] = m
	}
	return out
}

// Delete 删除历史记录。starred 的需要 force=true。
func (h *HistoryStore) Delete(ctx context.Context, id string, force bool) error {
	if h.db == nil {
		return ErrHistoryNotFound
	}

	if !force {
		if err := h.db.QueryRowContext(ctx, `SELECT id FROM task_history WHERE id = ?`, id).Scan(new(string)); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrHistoryNotFound
			}
			return fmt.Errorf("check history exists: %w", err)
		}
		var starred int
		// 任务级或任一段落被收藏即受保护
		if err := h.db.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM task_meta WHERE task_id = ? AND starred = 1)`, id).Scan(&starred); err != nil {
			return fmt.Errorf("check starred: %w", err)
		}
		if starred == 1 {
			return ErrStarredProtected.WithMessage("task is starred, use force=true to delete")
		}
	}

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	childTables := []string{
		"task_assignment",
		"task_report",
		"task_aggregated",
		"task_timeseries",
		"task_config_archive",
		"task_agent_events",
		"task_meta",
	}
	for _, tbl := range childTables {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE task_id = ?`, tbl), id); err != nil {
			return fmt.Errorf("delete %s: %w", tbl, err)
		}
	}

	result, err := tx.ExecContext(ctx, `DELETE FROM task_history WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete task_history: %w", err)
	}
	n, _ := result.RowsAffected() // 同上
	if n == 0 {
		return ErrHistoryNotFound
	}

	return tx.Commit()
}

// AppendTimeseries 追加时序趋势采样点。
func (h *HistoryStore) AppendTimeseries(ctx context.Context, taskID string, point HistoryTrendPoint) error {
	if h.db == nil {
		return nil
	}
	stageIndex := point.StageIndex
	if stageIndex == 0 {
		stageIndex = -1
	}
	_, err := h.db.ExecContext(ctx, `
		INSERT INTO task_timeseries (
			task_id, sampled_at, elapsed_sec, stage_index, total_qps, rtt_apdex, total_duration_apdex,
			rtt_avg_ms, rtt_p95_ms, rtt_p99_ms,
			total_duration_avg_ms, total_duration_p95_ms, total_duration_p99_ms,
			client_avg_ms, encode_avg_ms, decode_avg_ms,
			bots_running, bots_errored,
			send_kbps, recv_kbps, avg_cpu_percent, max_cpu_percent, avg_mem_percent, max_mem_percent,
			goroutines, threads, fds, online_count, offline_count
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, taskID, point.SampledAt, point.ElapsedSec, stageIndex, point.TotalQPS, point.RTTApdex, point.TotalDurationApdex,
		point.RTTAvgMs, point.RTTP95Ms, point.RTTP99Ms,
		point.TotalDurationAvgMs, point.TotalDurationP95Ms, point.TotalDurationP99Ms,
		point.ClientAvgMs, point.EncodeAvgMs, point.DecodeAvgMs,
		point.BotsRunning, point.BotsErrored,
		point.SendKBps, point.RecvKBps, point.AvgCPUPercent, point.MaxCPUPercent, point.AvgMemPercent, point.MaxMemPercent,
		point.Goroutines, point.Threads, point.FDs, point.OnlineCount, point.OfflineCount)
	return err
}

// PruneExpired 清理过期记录。ctx 用于取消进行中的 SQL 操作。
func (h *HistoryStore) PruneExpired(ctx context.Context, now time.Time) (int, error) {
	if h.db == nil {
		return 0, nil
	}

	cutoff := now.AddDate(0, 0, -h.cfg.RetentionDays)

	rows, err := h.db.QueryContext(ctx, `
		SELECT id FROM task_history th
		WHERE th.stopped_at IS NOT NULL AND th.stopped_at < ?
		  AND NOT EXISTS (SELECT 1 FROM task_meta m WHERE m.task_id = th.id AND m.starred = 1)
	`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune expired: query ids: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("prune expired: scan id: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()

	if len(ids) == 0 {
		return 0, nil
	}

	// 事务：子表 + 主表原子删除，避免中途失败导致数据不一致
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("prune expired: begin tx: %w", err)
	}
	defer tx.Rollback()

	childTables := []string{
		"task_assignment", "task_report", "task_aggregated",
		"task_timeseries", "task_config_archive", "task_agent_events", "task_meta",
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	for _, tbl := range childTables {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE task_id IN (%s)`, tbl, placeholders), args...); err != nil {
			return 0, fmt.Errorf("prune expired: delete %s: %w", tbl, err)
		}
	}

	// 按已筛出的 ids 删除主表（task_meta 已在上面删除，不能再依赖 starred 子查询）。
	result, err := tx.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM task_history WHERE id IN (%s)`, placeholders), args...)
	if err != nil {
		return 0, fmt.Errorf("prune expired: delete main: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("prune expired: commit: %w", err)
	}

	n, _ := result.RowsAffected() // 同上
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

	utils.GetWorkPool().GoWithStop(func(stopCh <-chan struct{}) {
		if _, err := h.PruneExpired(ctx, time.Now()); err != nil {
			stresslog.Error("历史清理失败", zap.Error(err))
		}

		ticker := time.NewTicker(h.prune)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			case <-ticker.C:
				if _, err := h.PruneExpired(ctx, time.Now()); err != nil {
					stresslog.Error("历史清理失败", zap.Error(err))
				}
			}
		}
	})
}

// Close 关闭数据库连接。
// cancel 会使 prune goroutine 中进行中的 ExecContext 立即中断，
// driver 自行关闭连接，避免与 db.Close 并发导致 WSASend SEGV。
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

	// 列名约定：task_history 走 th.*，元数据（starred/tags/note，stage_index=-1）走 m.*。
	if f.State != "" {
		conds = append(conds, "th.state = ?")
		args = append(args, f.State)
	}
	if !f.StartedAfter.IsZero() {
		conds = append(conds, "th.started_at >= ?")
		args = append(args, f.StartedAfter)
	}
	if !f.StartedBefore.IsZero() {
		conds = append(conds, "th.started_at < ?")
		args = append(args, f.StartedBefore)
	}
	if f.Starred != nil {
		if *f.Starred {
			// 任务级或任一阶段段落被收藏
			conds = append(conds, "EXISTS (SELECT 1 FROM task_meta sm WHERE sm.task_id = th.id AND sm.starred = 1)")
		} else {
			conds = append(conds, "NOT EXISTS (SELECT 1 FROM task_meta sm WHERE sm.task_id = th.id AND sm.starred = 1)")
		}
	}
	if f.Search != "" {
		conds = append(conds, "(th.name LIKE ? OR th.id LIKE ? OR m.note LIKE ? OR CAST(m.tags AS CHAR) LIKE ?)")
		pattern := "%" + f.Search + "%"
		args = append(args, pattern, pattern, pattern, pattern)
	}
	for _, tag := range f.Tags {
		conds = append(conds, "JSON_CONTAINS(m.tags, ?)")
		tagJSON, _ := json.Marshal(tag) // []string 序列化不会失败
		args = append(args, string(tagJSON))
	}
	for _, tag := range f.TagsAll {
		conds = append(conds, "JSON_CONTAINS(m.tags, ?)")
		tagJSON, _ := json.Marshal(tag) // []string 序列化不会失败
		args = append(args, string(tagJSON))
	}

	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	return where, args
}

// insertTaskReport 写入一条 task_report（指定 stage_index）。
func insertTaskReport(tx *sql.Tx, taskID, agentID, agentName string, report TaskCompletionReport, stageIndex int) error {
	snapJSON, _ := json.Marshal(report.FinalSnapshot)
	cleanupJSON, _ := json.Marshal(report.CleanupStatus)
	_, err := tx.Exec(`
		INSERT INTO task_report (task_id, agent_id, agent_name, result, error_msg, finished_at, final_snapshot, cleanup_status, stage_index)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, taskID, agentID, agentName, string(report.Result), report.ErrorMsg, report.FinishedAt, snapJSON, cleanupJSON, stageIndex)
	return err
}

// insertTaskAggregated 写入一条 task_aggregated（指定 stage_index，幂等覆盖）。
func insertTaskAggregated(tx *sql.Tx, taskID string, stageIndex int, stressJSON, sysJSON []byte) error {
	_, err := tx.Exec(`
		INSERT INTO task_aggregated (task_id, stage_index, final_stress, final_system)
		VALUES (?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE final_stress=VALUES(final_stress), final_system=VALUES(final_system)
	`, taskID, stageIndex, stressJSON, sysJSON)
	return err
}

func buildConfigSummary(task *Task) ConfigSummary {
	s := ConfigSummary{
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
