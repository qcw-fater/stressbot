package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"stressbot/admin/apierror"
	"stressbot/admin/history"
	admintask "stressbot/admin/task"
	json "stressbot/internal/jsonx"
)

// compareTarget 对比目标：任务 ID + 可选阶段段落号（-1 表示整体）。
type compareTarget struct {
	id         string
	stageIndex int
}

// parseCompareTargets 解析对比目标，支持新 targets=a:-1,b:2 与旧 ids=a,b。
func parseCompareTargets(q url.Values) ([]compareTarget, error) {
	raw := q.Get("targets")
	useTargets := raw != ""
	if !useTargets {
		raw = q.Get("ids")
	}
	if raw == "" {
		return nil, apierror.ErrInvalidArgument.WithMessage("ids or targets query param required")
	}

	parts := strings.Split(raw, ",")
	if len(parts) < 2 {
		return nil, apierror.ErrInvalidArgument.WithMessage("at least 2 targets required")
	}
	if len(parts) > 5 {
		return nil, apierror.ErrInvalidArgument.WithMessage("at most 5 targets for comparison")
	}

	targets := make([]compareTarget, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		t := compareTarget{stageIndex: -1}
		if useTargets {
			if idx := strings.LastIndex(p, ":"); idx >= 0 {
				t.id = strings.TrimSpace(p[:idx])
				if n, err := strconv.Atoi(strings.TrimSpace(p[idx+1:])); err == nil {
					t.stageIndex = n
				}
			} else {
				t.id = p
			}
		} else {
			t.id = p
		}
		if t.id == "" {
			return nil, apierror.ErrInvalidArgument.WithMessage("invalid compare target")
		}
		targets = append(targets, t)
	}
	if len(targets) < 2 {
		return nil, apierror.ErrInvalidArgument.WithMessage("at least 2 valid targets required")
	}
	return targets, nil
}

// ──────────────────────────────────────────────────
// 历史归档 Handlers
// ──────────────────────────────────────────────────

func (s *Handler) handleListHistory(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeError(w, apierror.ErrHistoryDisabled)
		return
	}

	q := r.URL.Query()
	filter := history.Filter{
		State:         q.Get("state"),
		Search:        q.Get("search"),
		Limit:         parseIntOrDefault(q.Get("limit"), 20),
		Offset:        parseIntOrDefault(q.Get("offset"), 0),
		OrderBy:       q.Get("orderBy"),
		StartedAfter:  parseTimeOrDefault(q.Get("startedAfter"), time.Time{}),
		StartedBefore: parseTimeOrDefault(q.Get("startedBefore"), time.Time{}),
	}
	if tags := parseTagsFromQuery(r, "tags"); len(tags) > 0 {
		filter.Tags = tags
	}
	if tagsAll := parseTagsFromQuery(r, "tagsAll"); len(tagsAll) > 0 {
		filter.TagsAll = tagsAll
	}
	if star := q.Get("starred"); star != "" {
		v := parseBoolOrDefault(star, false)
		filter.Starred = &v
	}
	filter.IncludeStages = parseBoolOrDefault(q.Get("includeStages"), false)

	resp, err := s.history.List(r.Context(), filter)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Handler) handleGetHistory(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeError(w, apierror.ErrHistoryDisabled)
		return
	}

	id := r.PathValue("id")
	stageIndex := parseIntOrDefault(r.URL.Query().Get("stageIndex"), -1)
	detail, err := s.history.GetDetailSummary(r.Context(), id, stageIndex)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Handler) handleGetHistoryAgents(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeError(w, apierror.ErrHistoryDisabled)
		return
	}

	id := r.PathValue("id")
	stageIndex := parseIntOrDefault(r.URL.Query().Get("stageIndex"), -1)
	reports, err := s.history.QueryReportSummaries(r.Context(), id, stageIndex)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, reports)
}

func (s *Handler) handleGetHistoryConfig(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeError(w, apierror.ErrHistoryDisabled)
		return
	}

	id := r.PathValue("id")
	cfg, err := s.history.GetConfigSummary(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Handler) handleGetHistoryConfigArchive(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeError(w, apierror.ErrHistoryDisabled)
		return
	}

	id := r.PathValue("id")
	archive, err := s.history.GetConfigArchive(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, archive)
}

func (s *Handler) handleGetHistoryTimeseries(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeError(w, apierror.ErrHistoryDisabled)
		return
	}

	id := r.PathValue("id")
	maxPoints := parseIntOrDefault(r.URL.Query().Get("maxPoints"), defaultHistoryTimeseriesMaxPoints)
	stageIndex := parseIntOrDefault(r.URL.Query().Get("stageIndex"), -1)
	resp, err := s.history.GetTimeseries(r.Context(), id, maxPoints, stageIndex)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Handler) handleGetHistoryTags(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeError(w, apierror.ErrHistoryDisabled)
		return
	}

	tags, err := s.history.AllTags(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

func (s *Handler) handleUpdateHistory(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeError(w, apierror.ErrHistoryDisabled)
		return
	}

	id := r.PathValue("id")
	var req history.UpdateHistoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apierror.ErrInvalidArgument.WithMessage("invalid json"))
		return
	}

	stageIndex := parseIntOrDefault(r.URL.Query().Get("stageIndex"), -1)
	// 统一写入：stageIndex<=0 → 任务级（-1），>=1 → 段落级，均存 task_meta。
	if err := s.history.UpsertMeta(r.Context(), id, stageIndex, req); err != nil {
		writeError(w, err)
		return
	}

	// 返回更新后的历史详情展示数据
	detail, err := s.history.GetDetailSummary(r.Context(), id, stageIndex)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Handler) handleDeleteHistory(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeError(w, apierror.ErrHistoryDisabled)
		return
	}

	id := r.PathValue("id")
	force := parseBoolOrDefault(r.URL.Query().Get("force"), false)

	if err := s.history.Delete(r.Context(), id, force); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Handler) handleCloneHistory(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeError(w, apierror.ErrHistoryDisabled)
		return
	}

	id := r.PathValue("id")

	// 解析可选的 name 覆盖
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, apierror.ErrInvalidArgument.WithMessage("invalid JSON body"))
		return
	}

	cfg, err := s.history.GetConfig(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}

	// 查找原始任务获取名称和 totalBots
	origName := ""
	totalBots := 0
	if orig, err := s.history.GetRecord(r.Context(), id); err == nil {
		origName = orig.Name
		totalBots = orig.TotalBots
	}

	if origName == "" {
		origName = id[:8]
	}

	cloneName := origName + " (clone)"
	if body.Name != "" {
		cloneName = body.Name
	}

	newTask := &admintask.Task{
		ID:        s.nextID(),
		Name:      cloneName,
		State:     admintask.Pending,
		TotalBots: totalBots,
		Config:    *cfg,
		CreatedAt: time.Now(),
	}
	if err := s.tasks.Create(newTask); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": newTask.ID})
}

func (s *Handler) handleCompareHistory(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeError(w, apierror.ErrHistoryDisabled)
		return
	}

	// 兼容两种入口：
	//   旧：ids=a,b          → 仅整体对比
	//   新：targets=a:-1,b:2 → 支持阶段段落对比（stageIndex），冒号后为段落号
	targets, err := parseCompareTargets(r.URL.Query())
	if err != nil {
		writeError(w, err)
		return
	}

	tasks := make([]history.CompareTask, 0, len(targets))
	for _, t := range targets {
		task, err := s.history.GetCompareTask(r.Context(), t.id, t.stageIndex)
		if err != nil {
			writeError(w, err)
			return
		}
		tasks = append(tasks, *task)
	}

	// 计算每个动作在多个任务间的 P99 对比
	diff := history.CompareDiff{Actions: make(map[string][]*float64)}
	for _, t := range tasks {
		for _, a := range t.FinalSnapshot.Actions {
			diff.Actions[a.Name] = append(diff.Actions[a.Name], a.RTT.P99Ms)
		}
	}

	writeJSON(w, http.StatusOK, history.CompareResponse{Tasks: tasks, Diff: diff})
}
