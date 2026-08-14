package httpapi

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	admintask "stressbot/admin/task"
	"stressbot/errcode"
	json "stressbot/internal/jsonx"
	"stressbot/internal/stresslog"

	"go.uber.org/zap"
)

// writeBaselineFiles 将上传的 flow/proto/scripts/adapter 写入磁盘基线目录，
// 使前端下次同步时本地资源与基线一致，不再误报冲突。
func (s *Handler) writeBaselineFiles(cfg *admintask.Config, flowData []byte) {
	if err := safeWriteFile("conf/flow", "flow.json", flowData); err != nil {
		stresslog.Warn("写入基线 flow.json 失败", zap.Error(err))
	}
	for name, data := range cfg.ProtoFiles {
		if err := safeWriteFile("conf/proto", name, data); err != nil {
			stresslog.Warn("写入基线 proto 失败",
				zap.String("name", name),
				zap.Error(err))
		}
	}
	for name, data := range cfg.LuaScripts {
		if err := safeWriteFile("conf/scripts", name, data); err != nil {
			stresslog.Warn("写入基线脚本失败",
				zap.String("name", name),
				zap.Error(err))
		}
	}
	// 当前 adapter 分发格式：每连接一份 *_codec.json + 共享 errors.json。
	for name, data := range cfg.Codecs {
		if err := safeWriteFile("conf/adapter", name, data); err != nil {
			stresslog.Warn("写入基线 codec 失败",
				zap.String("name", name),
				zap.Error(err))
		}
	}
	if len(cfg.ErrorMap) > 0 {
		if err := safeWriteFile("conf/adapter", "errors.json", cfg.ErrorMap); err != nil {
			stresslog.Warn("写入基线 errors.json 失败",
				zap.Error(err))
		}
	}
}

// safeWriteFile 将 data 写入 dir/name，自动创建目录，防止路径穿越。
func safeWriteFile(dir, name string, data []byte) error {
	name = filepath.Base(name)
	if name == "." || name == ".." {
		return fmt.Errorf("invalid file name: %s", name)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, name), data, 0644)
}

// ── 基线资源读取 ──

func (s *Handler) handleBaselineProtoIndex(w http.ResponseWriter, _ *http.Request) {
	files, err := baselineResources.List(resourceProto, ".proto")
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (s *Handler) handleBaselineProtoFile(w http.ResponseWriter, r *http.Request) {
	serveBaselineResource(w, r, resourceProto)
}

func (s *Handler) handleBaselineScriptIndex(w http.ResponseWriter, _ *http.Request) {
	files, err := baselineResources.List(resourceScripts, ".lua")
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (s *Handler) handleBaselineScriptFile(w http.ResponseWriter, r *http.Request) {
	serveBaselineResource(w, r, resourceScripts)
}

// handleBaselineCodecIndex 列出 adapter 基线目录下的 codec/errors 文件名（T3 前端基线同步枚举用）。
// 目录契约：conf/adapter 下只有 *_codec.json 与 errors.json（上传写入侧已拒绝其它）。
// handler 只按 .json 后缀如实列目录，不二次过滤文件名（前端按 errors.json/其余分类）。
func (s *Handler) handleBaselineCodecIndex(w http.ResponseWriter, _ *http.Request) {
	files, err := baselineResources.List(resourceAdapter, ".json")
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

// handleBaselineCodecFile 提供 adapter 基线目录下的单个 codec/errors 文件。
// 路径形如 /sbot/baseline/adapter/{name}，name = tcp_logic_codec.json / errors.json 等。
func (s *Handler) handleBaselineCodecFile(w http.ResponseWriter, r *http.Request) {
	serveBaselineResource(w, r, resourceAdapter)
}

func (s *Handler) handleErrorCodeIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(errcode.AllCodes()) // 写入 HTTP 响应，错误由 recoverMiddleware 兜底
}

func (s *Handler) handleBaselineFlow(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, baselineResources.FlowFile())
}

func serveBaselineResource(w http.ResponseWriter, r *http.Request, kind resourceKind) {
	filename, err := baselineResources.File(kind, r.PathValue("name"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	http.ServeFile(w, r, filename)
}
