package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"stressbot/admin/apierror"
)

func TestWriteErrorRecognizesWrappedAPIError(t *testing.T) {
	recorder := httptest.NewRecorder()
	err := fmt.Errorf("create task: %w", apierror.ErrInvalidArgument)

	WriteError(recorder, err)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}
