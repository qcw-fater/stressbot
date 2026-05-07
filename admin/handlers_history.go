package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// ──────────────────────────────────────────────────
// 历史归档 Handlers
// ──────────────────────────────────────────────────

func (s *AdminServer) handleListHistory(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeError(w, ErrHistoryDisabled)
		return
	}

	q := r.URL.Query()
	filter := HistoryFilter{
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

	resp, err := s.history.List(filter)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *AdminServer) handleGetHistory(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeError(w, ErrHistoryDisabled)
		return
	}

	id := r.PathValue("id")
	detail, err := s.history.Get(id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *AdminServer) handleGetHistoryAgents(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeError(w, ErrHistoryDisabled)
		return
	}

	id := r.PathValue("id")
	detail, err := s.history.Get(id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail.AgentReports)
}

func (s *AdminServer) handleGetHistoryConfig(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeError(w, ErrHistoryDisabled)
		return
	}

	id := r.PathValue("id")
	cfg, err := s.history.GetConfig(id)
	if err != nil {
		writeError(w, err)
		return
	}

	// 查询任务元信息补充到响应
	meta, _ := s.history.Get(id)
	name := ""
	var totalBots int
	if meta != nil {
		name = meta.Name
		totalBots = meta.TotalBots
	}

	// 前端期望 scripts 而非 luaScripts
	scripts := make(map[string]string)
	for k, v := range cfg.LuaScripts {
		scripts[k] = string(v)
	}
	protoFiles := make(map[string]string)
	for k, v := range cfg.ProtoFiles {
		protoFiles[k] = string(v)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"taskId":      id,
		"name":        name,
		"totalBots":   totalBots,
		"robotConfig": cfg.RobotConfig,
		"flowJson":    cfg.FlowJSON,
		"headerJson":  cfg.HeaderJSON,
		"protoFiles":  protoFiles,
		"scripts":     scripts,
	})
}

func (s *AdminServer) handleGetHistoryTimeseries(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeError(w, ErrHistoryDisabled)
		return
	}

	id := r.PathValue("id")
	resp, err := s.history.GetTimeseries(id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *AdminServer) handleGetHistoryTags(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeError(w, ErrHistoryDisabled)
		return
	}

	tags, err := s.history.AllTags()
	if err != nil {
		writeError(w, err)
		return
	}
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

func (s *AdminServer) handleUpdateHistory(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeError(w, ErrHistoryDisabled)
		return
	}

	id := r.PathValue("id")
	var req UpdateHistoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, ErrInvalidArgument.WithMessage("invalid json"))
		return
	}

	if err := s.history.UpdateMeta(id, req); err != nil {
		writeError(w, err)
		return
	}

	// 返回更新后的 HistoryDetail
	detail, err := s.history.Get(id)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *AdminServer) handleDeleteHistory(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeError(w, ErrHistoryDisabled)
		return
	}

	id := r.PathValue("id")
	force := parseBoolOrDefault(r.URL.Query().Get("force"), false)

	if err := s.history.Delete(id, force); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *AdminServer) handleCloneHistory(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeError(w, ErrHistoryDisabled)
		return
	}

	id := r.PathValue("id")

	// 解析可选的 name 覆盖
	var body struct {
		Name string `json:"name"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	cfg, err := s.history.GetConfig(id)
	if err != nil {
		writeError(w, err)
		return
	}

	// 查找原始任务获取名称和 totalBots
	origName := ""
	totalBots := 0
	if orig, err := s.history.Get(id); err == nil {
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

	newTask := &Task{
		ID:        generateID(),
		Name:      cloneName,
		State:     TaskPending,
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

func (s *AdminServer) handleCompareHistory(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeError(w, ErrHistoryDisabled)
		return
	}

	idsStr := r.URL.Query().Get("ids")
	if idsStr == "" {
		writeError(w, ErrInvalidArgument.WithMessage("ids query param required"))
		return
	}
	ids := strings.Split(idsStr, ",")
	if len(ids) < 2 {
		writeError(w, ErrInvalidArgument.WithMessage("at least 2 ids required"))
		return
	}
	if len(ids) > 5 {
		writeError(w, ErrInvalidArgument.WithMessage("at most 5 ids for comparison"))
		return
	}

	var tasks []HistoryDetail
	for _, id := range ids {
		detail, err := s.history.Get(strings.TrimSpace(id))
		if err != nil {
			writeError(w, err)
			return
		}
		tasks = append(tasks, *detail)
	}

	// 计算每个动作在多个任务间的 P99 对比
	diff := CompareDiff{Actions: make(map[string][]float64)}
	for _, t := range tasks {
		for _, a := range t.FinalSnapshot.Actions {
			diff.Actions[a.Name] = append(diff.Actions[a.Name], a.Latency.P99Ms)
		}
	}

	writeJSON(w, http.StatusOK, CompareResponse{Tasks: tasks, Diff: diff})
}
