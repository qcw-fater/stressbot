package apierror

import (
	"net/http"
	"testing"
)

func TestTemplateErrorsHaveStableStatusCodes(t *testing.T) {
	checks := map[*Error]int{
		ErrTemplateLibraryDisabled:  http.StatusServiceUnavailable,
		ErrActionTemplateNotFound:   http.StatusNotFound,
		ErrListenTemplateNotFound:   http.StatusNotFound,
		ErrTemplateNameConflict:     http.StatusConflict,
		ErrTemplateSnapshotConflict: http.StatusConflict,
	}
	for apiErr, want := range checks {
		if apiErr.HTTPStatus != want {
			t.Errorf("%s status=%d want=%d", apiErr.Code, apiErr.HTTPStatus, want)
		}
	}
}
