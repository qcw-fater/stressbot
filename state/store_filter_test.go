package state

import "testing"

func TestCompareValuesNotIn(t *testing.T) {
	list := []any{1, 2, "3"}
	if !CompareValues(4, list, "notIn") {
		t.Fatal("4 should not be in list")
	}
	if CompareValues(2, list, "notIn") {
		t.Fatal("2 should be in list")
	}
}

func TestCompareValuesContainsList(t *testing.T) {
	list := []any{1, "a", map[string]any{"id": 3}}
	if !CompareValues("alphabet", "a", "contains") {
		t.Fatal("string contains should keep existing behavior")
	}
	if !CompareValues(list, "a", "contains") {
		t.Fatal("list should contain a")
	}
	if CompareValues(list, "b", "contains") {
		t.Fatal("list should not contain b")
	}
	if !CompareValues(list, "b", "notContains") {
		t.Fatal("list should not contain b")
	}
	if CompareValues(list, "a", "notContains") {
		t.Fatal("list contains a, notContains should be false")
	}
}
