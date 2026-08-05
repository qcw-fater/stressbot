package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	"go.uber.org/zap"

	json "stressbot/utils/jsonx"
	stresslog "stressbot/utils/log"
)

type TemplateIDPolicy string

const (
	TemplateIDPreserve        TemplateIDPolicy = "preserve"
	TemplateIDGenerateMissing TemplateIDPolicy = "generate-missing"
)

type TemplateSnapshot[T any] struct {
	Revision string `json:"revision"`
	Items    []T    `json:"items"`
}

type ReplaceTemplateSnapshotRequest[T any] struct {
	ExpectedRevision string           `json:"expectedRevision"`
	IDPolicy         TemplateIDPolicy `json:"idPolicy"`
	Items            []T              `json:"items"`
}

type ReplaceTemplateSnapshotResponse[T any] struct {
	Revision string `json:"revision"`
	Count    int    `json:"count"`
	Items    []T    `json:"items"`
}

func computeTemplateSnapshotRevision[T any](items []T, id func(T) string) (string, error) {
	stable := append([]T(nil), items...)
	sort.Slice(stable, func(i, j int) bool { return id(stable[i]) < id(stable[j]) })
	data, err := json.Marshal(stable)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validateTemplateSnapshotIdentity(id, name string, policy TemplateIDPolicy, createdAt, updatedAt time.Time) error {
	if id != "" && len(id) > 32 {
		return ErrInvalidArgument.WithMessage("模板 ID 不能超过 32 个字符")
	}
	if policy == TemplateIDPreserve {
		if id == "" {
			return ErrInvalidArgument.WithMessage("完整恢复中的模板 ID 不能为空")
		}
		if createdAt.IsZero() || updatedAt.IsZero() {
			return ErrInvalidArgument.WithMessage("完整恢复中的模板时间无效")
		}
	}
	if policy != TemplateIDPreserve && policy != TemplateIDGenerateMissing {
		return ErrInvalidArgument.WithMessage("模板快照 idPolicy 无效")
	}
	if name == "" {
		return ErrInvalidArgument.WithMessage("模板名称不能为空")
	}
	return nil
}

const (
	templateNameMax          = 80
	templateDescriptionMax   = 500
	templateCRUDMaxBytes     = 1 << 20
	templateSnapshotMaxBytes = 50 << 20
)

type templateDefaultRef struct {
	Server    string          `json:"server"`
	Route     json.RawMessage `json:"route"`
	QueueSize *int            `json:"queueSize,omitempty"`
}

func normalizeTemplateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ErrInvalidArgument.WithMessage("模板名称不能为空")
	}
	if utf8.RuneCountInString(name) > templateNameMax {
		return "", ErrInvalidArgument.WithMessage(fmt.Sprintf("模板名称不能超过 %d 个字符", templateNameMax))
	}
	return name, nil
}

func normalizeTemplateDescription(description string) (string, error) {
	if utf8.RuneCountInString(description) > templateDescriptionMax {
		return "", ErrInvalidArgument.WithMessage(fmt.Sprintf("模板描述不能超过 %d 个字符", templateDescriptionMax))
	}
	return description, nil
}

func requireJSONObject(raw json.RawMessage, label string) (map[string]json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return nil, ErrInvalidArgument.WithMessage(label + "必须是 JSON 对象")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, ErrInvalidArgument.WithMessage(label + "必须是 JSON 对象")
	}
	return object, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableJSON(value json.RawMessage) any {
	if len(strings.TrimSpace(string(value))) == 0 {
		return nil
	}
	return []byte(value)
}

func mapTemplateWriteError(err error) error {
	if err == nil {
		return nil
	}
	if apiErr, ok := errors.AsType[*Error](err); ok {
		return apiErr
	}
	if mysqlErr, ok := errors.AsType[*mysql.MySQLError](err); ok && mysqlErr.Number == 1062 {
		return ErrTemplateNameConflict
	}
	stresslog.Error("[ADMIN] 模板库数据库操作失败", zap.Error(err))
	return ErrInternal.WithMessage("模板库操作失败")
}

func writeTemplateStoreError(w http.ResponseWriter, err error) {
	err = mapTemplateWriteError(err)
	if apiErr, ok := errors.AsType[*Error](err); ok && apiErr.Code == ErrTemplateNameConflict.Code {
		err = apiErr.WithMessage("同类模板名称已存在")
	}
	writeError(w, err)
}

func decodeTemplateJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64, message string) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(dst); err != nil {
		return ErrInvalidArgument.WithMessage(message)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrInvalidArgument.WithMessage(message)
	}
	return nil
}
