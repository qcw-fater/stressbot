package httpapi

import (
	"errors"
	"io"
	"net/http"

	"stressbot/admin/apierror"
	"stressbot/admin/template"
	json "stressbot/internal/jsonx"
)

// 管理面 JSON 请求体上限，分两档。裸 Decode 不设上限时，单个请求就能把
// 任意大的 body 读进 Admin 内存；multipart 上传已有 32MB 上限（见 task_routes.go），
// JSON 这边按实际载荷给更紧的值：
//   - 常规请求体（模板/流程增改、重命名打标签、克隆、codec 预览）只承载
//     少量字段，1MB 绰绰有余，超出即为畸形或恶意请求；
//   - 快照类请求体（整库流程/模板导入）承载一整份备份，与文件上传同量级，给 50MB。
const (
	jsonBodyMaxBytes     = 1 << 20
	jsonSnapshotMaxBytes = 50 << 20
)

func mapTemplateWriteError(err error) error {
	return template.MapWriteError(err)
}

func writeTemplateStoreError(w http.ResponseWriter, err error) {
	err = mapTemplateWriteError(err)
	if apiErr, ok := errors.AsType[*apierror.Error](err); ok && apiErr.Code == apierror.ErrTemplateNameConflict.Code {
		err = apiErr.WithMessage("同类模板名称已存在")
	}
	writeError(w, err)
}

// decodeJSONBody 限长解码请求体，并拒绝尾随内容（一个请求只允许一个 JSON 值）。
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64, message string) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(dst); err != nil {
		return apierror.ErrInvalidArgument.WithMessage(message)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return apierror.ErrInvalidArgument.WithMessage(message)
	}
	return nil
}
