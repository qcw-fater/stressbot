package history

import (
	"strings"
	"testing"
)

func TestTaskConfigArchiveUpsertUpdatesEveryConfigColumn(t *testing.T) {
	normalized := strings.ToLower(strings.Join(strings.Fields(TaskConfigArchiveUpsertSQL), " "))
	for _, column := range []string{
		"flow_json",
		"proto_files",
		"lua_scripts",
		"codecs",
		"error_map",
		"robot_config",
	} {
		want := column + "=values(" + column + ")"
		if !strings.Contains(normalized, want) {
			t.Errorf("UPSERT does not update %s", column)
		}
	}
}
