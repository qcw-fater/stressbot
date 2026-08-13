package task

import "testing"

func TestTaskAssignmentValidateRequiresStartIndex(t *testing.T) {
	assignment := TaskAssignment{}

	if err := assignment.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want missing startIndex error")
	}
}

func TestTaskAssignmentValidateAcceptsZeroStartIndex(t *testing.T) {
	startIndex := 0
	assignment := TaskAssignment{StartIndex: &startIndex}

	if err := assignment.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}
