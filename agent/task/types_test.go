package task

import "testing"

func TestAssignmentValidateRequiresStartIndex(t *testing.T) {
	assignment := Assignment{}

	if err := assignment.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want missing startIndex error")
	}
}

func TestAssignmentValidateAcceptsZeroStartIndex(t *testing.T) {
	startIndex := 0
	assignment := Assignment{StartIndex: &startIndex}

	if err := assignment.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestPackageScopedTaskNames(t *testing.T) {
	startIndex := 0
	assignment := Assignment{StartIndex: &startIndex}
	if err := assignment.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
	if runner := NewRunner(&assignment, nil); runner == nil {
		t.Fatal("NewRunner() = nil")
	}
	var _ CompletionReport
}
