package history

import (
	"strings"
	"time"

	json "stressbot/internal/jsonx"
)

const (
	DefaultTimeseriesMaxPoints = 600
	MaxTimeseriesMaxPoints     = 2000
)

type Filter struct {
	State         string
	StartedAfter  time.Time
	StartedBefore time.Time
	Tags          []string
	TagsAll       []string
	Starred       *bool
	Search        string
}

func BuildWhere(filter Filter) (string, []any) {
	conditions := make([]string, 0)
	args := make([]any, 0)
	if filter.State != "" {
		conditions = append(conditions, "th.state = ?")
		args = append(args, filter.State)
	}
	if !filter.StartedAfter.IsZero() {
		conditions = append(conditions, "th.started_at >= ?")
		args = append(args, filter.StartedAfter)
	}
	if !filter.StartedBefore.IsZero() {
		conditions = append(conditions, "th.started_at < ?")
		args = append(args, filter.StartedBefore)
	}
	if filter.Starred != nil {
		if *filter.Starred {
			conditions = append(conditions, "EXISTS (SELECT 1 FROM task_meta sm WHERE sm.task_id = th.id AND sm.starred = 1)")
		} else {
			conditions = append(conditions, "NOT EXISTS (SELECT 1 FROM task_meta sm WHERE sm.task_id = th.id AND sm.starred = 1)")
		}
	}
	if filter.Search != "" {
		conditions = append(conditions, "(th.name LIKE ? OR th.id LIKE ? OR m.note LIKE ? OR CAST(m.tags AS CHAR) LIKE ?)")
		pattern := "%" + filter.Search + "%"
		args = append(args, pattern, pattern, pattern, pattern)
	}
	for _, tag := range append(append([]string(nil), filter.Tags...), filter.TagsAll...) {
		conditions = append(conditions, "JSON_CONTAINS(m.tags, ?)")
		tagJSON, _ := json.Marshal(tag)
		args = append(args, string(tagJSON))
	}
	if len(conditions) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

func NormalizeMaxPoints(maxPoints int) int {
	if maxPoints <= 0 {
		return DefaultTimeseriesMaxPoints
	}
	if maxPoints > MaxTimeseriesMaxPoints {
		return MaxTimeseriesMaxPoints
	}
	return maxPoints
}

func Sample[T any](points []T, maxPoints int) []T {
	if len(points) <= maxPoints {
		return points
	}
	if maxPoints <= 1 {
		return points[len(points)-1:]
	}
	result := make([]T, 0, maxPoints)
	lastIndex := -1
	for i := range maxPoints {
		index := int(float64(i) * float64(len(points)-1) / float64(maxPoints-1))
		if index == lastIndex {
			continue
		}
		result = append(result, points[index])
		lastIndex = index
	}
	return result
}
