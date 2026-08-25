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

// IDPolicy 为快照恢复的 ID 处理策略：IDPreserve 完整恢复（条目必须自带 ID 与时间戳），
// IDGenerateMissing 合并恢复（缺 ID 的条目生成新 ID，已有 ID 须存在于目标库）。
// NameMax/DescriptionMax 为模板名称与描述的长度上限（字符数）。
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

// ComputeRevision 将 items 按 id 升序排序后序列化并取 sha256，
// 得到与输入顺序无关的集合内容版本号（"sha256:" 前缀）。
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

// ValidateIdentity 校验快照条目的身份字段：ID 长度不超过 32、idPolicy 取值合法、名称非空；
// IDPreserve 策略下还要求条目自带 ID 且创建/更新时间齐备。
func ValidateIdentity(id, name string, policy IDPolicy, createdAt, updatedAt time.Time) error {
	if id != "" && len(id) > 32 {
		return errors.New("模板 ID 不能超过 32 个字符")
	}
	if policy == IDPreserve {
		if id == "" {
			return errors.New("完整恢复中的模板 ID 不能为空")
		}
		if createdAt.IsZero() || updatedAt.IsZero() {
			return errors.New("完整恢复中的模板时间无效")
		}
	}
	if policy != IDPreserve && policy != IDGenerateMissing {
		return errors.New("模板快照 idPolicy 无效")
	}
	if name == "" {
		return errors.New("模板名称不能为空")
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

// NormalizeName 去除首尾空白并校验模板名称：非空且不超过 NameMax 个字符。
func NormalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("模板名称不能为空")
	}
	if utf8.RuneCountInString(name) > NameMax {
		return "", fmt.Errorf("模板名称不能超过 %d 个字符", NameMax)
	}
	return name, nil
}

// NormalizeDescription 校验模板描述不超过 DescriptionMax 个字符（保留原文，不去除空白）。
func NormalizeDescription(description string) (string, error) {
	if utf8.RuneCountInString(description) > DescriptionMax {
		return "", fmt.Errorf("模板描述不能超过 %d 个字符", DescriptionMax)
	}
	return description, nil
}

// RequireJSONObject 校验 raw 是非空 JSON 对象并按字段名返回解构结果；
// label 用于拼接"xxx必须是 JSON 对象"错误文案。
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
