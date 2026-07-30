package admin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"

	"stressbot/engine"
	json "stressbot/utils/jsonx"
)

var validActionTemplateSave = ActionTemplateSaveRequest{
	Name:    "登录请求",
	Pattern: engine.PatternTCPRequest,
	Data:    json.RawMessage(`{"pattern":"tcpRequest","service":"logic","route":{"cmd":1},"s2cProto":"LoginS2C"}`),
}

func TestValidateActionTemplate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ActionTemplateSaveRequest)
	}{
		{name: "blank name", mutate: func(r *ActionTemplateSaveRequest) { r.Name = "  " }},
		{name: "long name", mutate: func(r *ActionTemplateSaveRequest) { r.Name = strings.Repeat("界", 81) }},
		{name: "long description", mutate: func(r *ActionTemplateSaveRequest) { r.Description = strings.Repeat("描", 501) }},
		{name: "non object", mutate: func(r *ActionTemplateSaveRequest) { r.Data = json.RawMessage(`[]`) }},
		{name: "invalid pattern", mutate: func(r *ActionTemplateSaveRequest) { r.Pattern = "other" }},
		{name: "mismatched pattern", mutate: func(r *ActionTemplateSaveRequest) { r.Pattern = engine.PatternUDPSend }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validActionTemplateSave
			tt.mutate(&req)
			if _, err := validateActionTemplateSave(req); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}

	invalidBodies := []struct {
		name    string
		pattern string
		data    string
	}{
		{name: "missing service", pattern: engine.PatternTCPRequest, data: `{"pattern":"tcpRequest","route":{},"s2cProto":"LoginS2C"}`},
		{name: "missing route", pattern: engine.PatternTCPRequest, data: `{"pattern":"tcpRequest","service":"logic","s2cProto":"LoginS2C"}`},
		{name: "missing address", pattern: engine.PatternTCPConnect, data: `{"pattern":"tcpConnect","service":"logic"}`},
		{name: "missing c2s proto", pattern: engine.PatternTCPSend, data: `{"pattern":"tcpSend","service":"logic","route":{}}`},
		{name: "missing s2c proto", pattern: engine.PatternTCPRequest, data: `{"pattern":"tcpRequest","service":"logic","route":{}}`},
		{name: "blank lua script", pattern: engine.PatternLua, data: `{"pattern":"lua","script":"  "}`},
		{name: "missing clear state keys", pattern: engine.PatternClearState, data: `{"pattern":"clearState","keys":[]}`},
		{name: "missing http url", pattern: engine.PatternHTTPRequest, data: `{"pattern":"httpRequest"}`},
		{name: "invalid http method", pattern: engine.PatternHTTPRequest, data: `{"pattern":"httpRequest","url":"https://example.com","method":"PUT"}`},
		{name: "invalid http content type", pattern: engine.PatternHTTPRequest, data: `{"pattern":"httpRequest","url":"https://example.com","contentType":"xml"}`},
	}
	for _, tt := range invalidBodies {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateActionTemplateSave(ActionTemplateSaveRequest{
				Name:    "无效模板",
				Pattern: tt.pattern,
				Data:    json.RawMessage(tt.data),
			})
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}

	normalized, err := validateActionTemplateSave(ActionTemplateSaveRequest{
		Name:    "  登录请求  ",
		Pattern: engine.PatternTCPRequest,
		Data:    validActionTemplateSave.Data,
	})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Name != "登录请求" {
		t.Fatalf("name = %q", normalized.Name)
	}
}

func TestActionTemplateStoreCreateAndList(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewActionTemplateStore(db)

	mock.ExpectExec(`(?s)INSERT INTO action_template`).
		WithArgs(sqlmock.AnyArg(), "登录请求", nil, engine.PatternTCPRequest, []byte(validActionTemplateSave.Data), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	created, err := store.Create(context.Background(), validActionTemplateSave)
	if err != nil {
		t.Fatal(err)
	}
	if len(created.ID) != 32 || created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("created = %#v", created)
	}

	now := time.Now()
	mock.ExpectQuery(`(?s)SELECT id, name, description, pattern, data_json, created_at, updated_at.*FROM action_template`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "description", "pattern", "data_json", "created_at", "updated_at"}).
			AddRow("a", "A", nil, engine.PatternTCPRequest, []byte(validActionTemplateSave.Data), now, now))
	items, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "a" {
		t.Fatalf("items = %#v", items)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestActionTemplateStoreUpdateNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectExec(`(?s)UPDATE action_template`).WillReturnResult(sqlmock.NewResult(0, 0))
	_, err = NewActionTemplateStore(db).Update(context.Background(), "missing", validActionTemplateSave)
	if !errors.Is(err, ErrActionTemplateNotFound) {
		t.Fatalf("error = %v", err)
	}
}

func TestMapTemplateWriteErrorMapsDuplicateName(t *testing.T) {
	err := mapTemplateWriteError(&mysql.MySQLError{Number: 1062, Message: "duplicate"})
	if !errors.Is(err, ErrTemplateNameConflict) {
		t.Fatalf("error = %v", err)
	}
}
