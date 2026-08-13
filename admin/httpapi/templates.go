package httpapi

import (
	"net/http"

	"stressbot/admin/apierror"
	"stressbot/admin/template"
	json "stressbot/internal/jsonx"
)

func (s *Handler) actionTemplateStore(w http.ResponseWriter) *template.ActionTemplateStore {
	if s.actionTemplates == nil {
		writeError(w, apierror.ErrTemplateLibraryDisabled.WithMessage("服务器未启用模板库"))
		return nil
	}
	return s.actionTemplates
}

func (s *Handler) handleListActionTemplates(w http.ResponseWriter, r *http.Request) {
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
func (s *Handler) handleCreateActionTemplate(w http.ResponseWriter, r *http.Request) {
	store := s.actionTemplateStore(w)
	if store == nil {
		return
	}
	var req template.ActionTemplateSaveRequest
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
func (s *Handler) handleGetActionTemplate(w http.ResponseWriter, r *http.Request) {
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
func (s *Handler) handleUpdateActionTemplate(w http.ResponseWriter, r *http.Request) {
	store := s.actionTemplateStore(w)
	if store == nil {
		return
	}
	var req template.ActionTemplateSaveRequest
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
func (s *Handler) handleDeleteActionTemplate(w http.ResponseWriter, r *http.Request) {
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

func (s *Handler) handleGetActionTemplateSnapshot(w http.ResponseWriter, r *http.Request) {
	store := s.actionTemplateStore(w)
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
func (s *Handler) handleReplaceActionTemplateSnapshot(w http.ResponseWriter, r *http.Request) {
	store := s.actionTemplateStore(w)
	if store == nil {
		return
	}
	var req template.ReplaceSnapshotRequest[template.ActionTemplate]
	if err := decodeTemplateJSON(w, r, &req, templateSnapshotMaxBytes, "动作模板快照不是合法 JSON"); err != nil {
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

func (s *Handler) listenTemplateStore(w http.ResponseWriter) *template.ListenTemplateStore {
	if s.listenTemplates == nil {
		writeError(w, apierror.ErrTemplateLibraryDisabled.WithMessage("服务器未启用模板库"))
		return nil
	}
	return s.listenTemplates
}
func (s *Handler) handleListListenTemplates(w http.ResponseWriter, r *http.Request) {
	store := s.listenTemplateStore(w)
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
func (s *Handler) handleCreateListenTemplate(w http.ResponseWriter, r *http.Request) {
	store := s.listenTemplateStore(w)
	if store == nil {
		return
	}
	var req template.ListenTemplateSaveRequest
	if err := decodeTemplateJSON(w, r, &req, templateCRUDMaxBytes, "监听模板不是合法 JSON"); err != nil {
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
func (s *Handler) handleGetListenTemplate(w http.ResponseWriter, r *http.Request) {
	store := s.listenTemplateStore(w)
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
func (s *Handler) handleUpdateListenTemplate(w http.ResponseWriter, r *http.Request) {
	store := s.listenTemplateStore(w)
	if store == nil {
		return
	}
	var req template.ListenTemplateSaveRequest
	if err := decodeTemplateJSON(w, r, &req, templateCRUDMaxBytes, "监听模板不是合法 JSON"); err != nil {
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
func (s *Handler) handleDeleteListenTemplate(w http.ResponseWriter, r *http.Request) {
	store := s.listenTemplateStore(w)
	if store == nil {
		return
	}
	if err := store.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeTemplateStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Handler) handleGetListenTemplateSnapshot(w http.ResponseWriter, r *http.Request) {
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
func (s *Handler) handleReplaceListenTemplateSnapshot(w http.ResponseWriter, r *http.Request) {
	store := s.listenTemplateStore(w)
	if store == nil {
		return
	}
	var req template.ReplaceSnapshotRequest[template.ListenTemplate]
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

func (s *Handler) handleListFlows(w http.ResponseWriter, r *http.Request) {
	if s.flows == nil {
		writeError(w, apierror.ErrFlowLibraryDisabled.WithMessage("服务器未启用流程库"))
		return
	}
	items, err := s.flows.List(r.Context())
	if err != nil {
		writeError(w, apierror.ErrInternal.WithMessage(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Handler) handleCreateFlow(w http.ResponseWriter, r *http.Request) {
	if s.flows == nil {
		writeError(w, apierror.ErrFlowLibraryDisabled.WithMessage("服务器未启用流程库"))
		return
	}
	var req template.FlowTemplateSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apierror.ErrInvalidArgument.WithMessage("invalid json"))
		return
	}
	created, err := s.flows.Create(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Handler) handleGetFlow(w http.ResponseWriter, r *http.Request) {
	if s.flows == nil {
		writeError(w, apierror.ErrFlowLibraryDisabled.WithMessage("服务器未启用流程库"))
		return
	}
	d, err := s.flows.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Handler) handleUpdateFlow(w http.ResponseWriter, r *http.Request) {
	if s.flows == nil {
		writeError(w, apierror.ErrFlowLibraryDisabled.WithMessage("服务器未启用流程库"))
		return
	}
	var req template.FlowTemplateSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apierror.ErrInvalidArgument.WithMessage("invalid json"))
		return
	}
	updated, err := s.flows.Update(r.Context(), r.PathValue("id"), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Handler) handleDeleteFlow(w http.ResponseWriter, r *http.Request) {
	if s.flows == nil {
		writeError(w, apierror.ErrFlowLibraryDisabled.WithMessage("服务器未启用流程库"))
		return
	}
	if err := s.flows.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Handler) handleGetFlowSnapshot(w http.ResponseWriter, r *http.Request) {
	if s.flows == nil {
		writeError(w, apierror.ErrFlowLibraryDisabled.WithMessage("服务器未启用流程库"))
		return
	}
	snapshot, err := s.flows.Snapshot(r.Context())
	if err != nil {
		writeError(w, apierror.ErrInternal.WithMessage(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Handler) handleReplaceFlowSnapshot(w http.ResponseWriter, r *http.Request) {
	if s.flows == nil {
		writeError(w, apierror.ErrFlowLibraryDisabled.WithMessage("服务器未启用流程库"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 50<<20)
	var req template.ReplaceFlowSnapshotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apierror.ErrInvalidArgument.WithMessage("备份中的流程快照不是合法 JSON"))
		return
	}
	resp, err := s.flows.ReplaceSnapshot(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
