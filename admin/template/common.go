package template

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"stressbot/admin/apierror"
	json "stressbot/internal/jsonx"
	"stressbot/internal/stresslog"

	"github.com/go-sql-driver/mysql"
	"go.uber.org/zap"
)

// IDPolicy controls how IDs are handled when restoring a template snapshot.
type IDPolicy string

const (
	IDPreserve        IDPolicy = "preserve"
	IDGenerateMissing IDPolicy = "generate-missing"
	NameMax                    = 80
	DescriptionMax             = 500
)

// Snapshot is a revisioned collection of templates.
type Snapshot[T any] struct {
	Revision string `json:"revision"`
	Items    []T    `json:"items"`
}

// ReplaceSnapshotRequest describes an optimistic snapshot replacement.
type ReplaceSnapshotRequest[T any] struct {
	ExpectedRevision string   `json:"expectedRevision"`
	IDPolicy         IDPolicy `json:"idPolicy"`
	Items            []T      `json:"items"`
}

// ReplaceSnapshotResponse reports the stored revision and normalized items.
type ReplaceSnapshotResponse[T any] struct {
	Revision string `json:"revision"`
	Count    int    `json:"count"`
	Items    []T    `json:"items"`
}

type templateDefaultRef struct {
	Server    string          `json:"server"`
	Route     json.RawMessage `json:"route"`
	QueueSize *int            `json:"queueSize,omitempty"`
}

func ComputeRevision[T any](items []T, id func(T) string) (string, error) {
	stable := append([]T(nil), items...)
	sort.Slice(stable, func(i, j int) bool { return id(stable[i]) < id(stable[j]) })
	data, err := json.Marshal(stable)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func ValidateIdentity(id, name string, policy IDPolicy, createdAt, updatedAt time.Time) error {
	if id != "" && len(id) > 32 {
		return fmt.Errorf("模板 ID 不能超过 32 个字符")
	}
	if policy == IDPreserve {
		if id == "" {
			return fmt.Errorf("完整恢复中的模板 ID 不能为空")
		}
		if createdAt.IsZero() || updatedAt.IsZero() {
			return fmt.Errorf("完整恢复中的模板时间无效")
		}
	}
	if policy != IDPreserve && policy != IDGenerateMissing {
		return fmt.Errorf("模板快照 idPolicy 无效")
	}
	if name == "" {
		return fmt.Errorf("模板名称不能为空")
	}
	return nil
}

func computeTemplateSnapshotRevision[T any](items []T, id func(T) string) (string, error) {
	return ComputeRevision(items, id)
}

func validateTemplateSnapshotIdentity(id, name string, policy IDPolicy, createdAt, updatedAt time.Time) error {
	if err := ValidateIdentity(id, name, policy, createdAt, updatedAt); err != nil {
		return apierror.ErrInvalidArgument.WithMessage(err.Error())
	}
	return nil
}

func normalizeTemplateName(name string) (string, error) {
	normalized, err := NormalizeName(name)
	if err != nil {
		return "", apierror.ErrInvalidArgument.WithMessage(err.Error())
	}
	return normalized, nil
}

func normalizeTemplateDescription(description string) (string, error) {
	normalized, err := NormalizeDescription(description)
	if err != nil {
		return "", apierror.ErrInvalidArgument.WithMessage(err.Error())
	}
	return normalized, nil
}

func requireJSONObject(raw json.RawMessage, label string) (map[string]json.RawMessage, error) {
	object, err := RequireJSONObject(raw, label)
	if err != nil {
		return nil, apierror.ErrInvalidArgument.WithMessage(err.Error())
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
	if apiErr, ok := errors.AsType[*apierror.Error](err); ok {
		return apiErr
	}
	if mysqlErr, ok := errors.AsType[*mysql.MySQLError](err); ok && mysqlErr.Number == 1062 {
		return apierror.ErrTemplateNameConflict
	}
	stresslog.Error("[ADMIN] 模板库数据库操作失败", zap.Error(err))
	return apierror.ErrInternal.WithMessage("模板库操作失败")
}

// MapWriteError translates database failures to the Admin API error contract.
func MapWriteError(err error) error { return mapTemplateWriteError(err) }

func NormalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("模板名称不能为空")
	}
	if utf8.RuneCountInString(name) > NameMax {
		return "", fmt.Errorf("模板名称不能超过 %d 个字符", NameMax)
	}
	return name, nil
}

func NormalizeDescription(description string) (string, error) {
	if utf8.RuneCountInString(description) > DescriptionMax {
		return "", fmt.Errorf("模板描述不能超过 %d 个字符", DescriptionMax)
	}
	return description, nil
}

func RequireJSONObject(raw json.RawMessage, label string) (map[string]json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return nil, fmt.Errorf("%s必须是 JSON 对象", label)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%s必须是 JSON 对象", label)
	}
	return object, nil
}
