package engine

import "testing"

func TestNavigatePathValuesWildcard(t *testing.T) {
	item := map[string]any{
		"shopData": []any{
			map[string]any{"ID": 1, "Count": 10},
			map[string]any{"ID": 2, "Count": 20},
		},
	}

	values := navigatePathValues(item, "shopData[].ID")
	if len(values) != 2 || values[0] != 1 || values[1] != 2 {
		t.Fatalf("unexpected values: %#v", values)
	}
}

func TestMatchFilterValuesModes(t *testing.T) {
	values := []any{1, 2, 3}

	if !matchFilterValues(values, 2, "eq", FilterModeAny) {
		t.Fatal("any should match when one value equals target")
	}
	if matchFilterValues(values, 2, "eq", FilterModeAll) {
		t.Fatal("all should not match when only one value equals target")
	}
	if !matchFilterValues(values, 4, "eq", FilterModeNone) {
		t.Fatal("none should match when no value equals target")
	}
	if matchFilterValues(values, 2, "eq", FilterModeNone) {
		t.Fatal("none should not match when one value equals target")
	}
}

func TestMatchFilterValuesEmptyModes(t *testing.T) {
	if matchFilterValues(nil, 1, "eq", FilterModeAny) {
		t.Fatal("empty any should be false")
	}
	if matchFilterValues(nil, 1, "eq", FilterModeAll) {
		t.Fatal("empty all should be false")
	}
	if !matchFilterValues(nil, 1, "eq", FilterModeNone) {
		t.Fatal("empty none should be true")
	}
}

func TestMatchFiltersWildcardNone(t *testing.T) {
	ae := &ActionExecutor{}
	item := map[string]any{
		"shopData": []any{
			map[string]any{"ID": 2},
			map[string]any{"ID": 3},
		},
	}

	ok := ae.matchFilters(item, []FilterDef{{Path: "shopData[].ID", Op: "eq", Value: 1, Mode: FilterModeNone}})
	if !ok {
		t.Fatal("item without ID=1 should pass none filter")
	}

	item["shopData"] = []any{
		map[string]any{"ID": 1},
		map[string]any{"ID": 3},
	}
	ok = ae.matchFilters(item, []FilterDef{{Path: "shopData[].ID", Op: "eq", Value: 1, Mode: FilterModeNone}})
	if ok {
		t.Fatal("item with ID=1 should not pass none filter")
	}
}
