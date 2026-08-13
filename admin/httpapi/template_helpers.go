package httpapi

import (
	"errors"
	"io"
	"net/http"

	"stressbot/admin/apierror"
	"stressbot/admin/template"
	json "stressbot/internal/jsonx"
)

const (
	templateCRUDMaxBytes     = 1 << 20
	templateSnapshotMaxBytes = 50 << 20
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

func decodeTemplateJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64, message string) error {
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
