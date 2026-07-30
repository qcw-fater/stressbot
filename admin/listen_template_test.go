package admin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	json "stressbot/utils/jsonx"
)

var validListenTemplateSave = ListenTemplateSaveRequest{
	Name: "登录推送",
	Kind: "declarative",
	Data: json.RawMessage(`{"s2cProto":"","store":[]}`),
}

func TestValidateListenTemplate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ListenTemplateSaveRequest)
	}{
		{name: "blank name", mutate: func(r *ListenTemplateSaveRequest) { r.Name = "" }},
		{name: "long name", mutate: func(r *ListenTemplateSaveRequest) { r.Name = strings.Repeat("界", 81) }},
		{name: "long description", mutate: func(r *ListenTemplateSaveRequest) { r.Description = strings.Repeat("描", 501) }},
		{name: "non object", mutate: func(r *ListenTemplateSaveRequest) { r.Data = json.RawMessage(`null`) }},
		{name: "invalid kind", mutate: func(r *ListenTemplateSaveRequest) { r.Kind = "other" }},
		{name: "mismatched kind", mutate: func(r *ListenTemplateSaveRequest) { r.Kind = "lua" }},
		{name: "blank lua script", mutate: func(r *ListenTemplateSaveRequest) {
			r.Kind = "lua"
			r.Data = json.RawMessage(`{"script":"  "}`)
		}},
		{name: "non object default ref", mutate: func(r *ListenTemplateSaveRequest) { r.DefaultRef = json.RawMessage(`[]`) }},
		{name: "blank default server", mutate: func(r *ListenTemplateSaveRequest) { r.DefaultRef = json.RawMessage(`{"server":"","route":{"cmd":1}}`) }},
		{name: "missing route", mutate: func(r *ListenTemplateSaveRequest) { r.DefaultRef = json.RawMessage(`{"server":"tcp:logic"}`) }},
		{name: "null route with whitespace", mutate: func(r *ListenTemplateSaveRequest) {
			r.DefaultRef = json.RawMessage(`{"server":"tcp:logic","route":  null }`)
		}},
		{name: "bad queue size", mutate: func(r *ListenTemplateSaveRequest) {
			r.DefaultRef = json.RawMessage(`{"server":"tcp:logic","route":{},"queueSize":0}`)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validListenTemplateSave
			tt.mutate(&req)
			if _, err := validateListenTemplateSave(req); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	if _, err := validateListenTemplateSave(validListenTemplateSave); err != nil {
		t.Fatal(err)
	}
}

func TestListenTemplateStoreCreateAndList(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewListenTemplateStore(db)
	mock.ExpectExec(`(?s)INSERT INTO listen_template`).
		WithArgs(sqlmock.AnyArg(), "登录推送", nil, "declarative", []byte(validListenTemplateSave.Data), nil, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	created, err := store.Create(context.Background(), validListenTemplateSave)
	if err != nil {
		t.Fatal(err)
	}
	if len(created.ID) != 32 {
		t.Fatalf("id = %q", created.ID)
	}

	now := time.Now()
	mock.ExpectQuery(`(?s)SELECT id, name, description, kind, data_json, default_ref_json, created_at, updated_at.*FROM listen_template`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "description", "kind", "data_json", "default_ref_json", "created_at", "updated_at"}).
			AddRow("l", "L", nil, "declarative", []byte(validListenTemplateSave.Data), nil, now, now))
	items, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "l" {
		t.Fatalf("items = %#v", items)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListenTemplateStoreDeleteNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectExec(`DELETE FROM listen_template`).WillReturnResult(sqlmock.NewResult(0, 0))
	err = NewListenTemplateStore(db).Delete(context.Background(), "missing")
	if !errors.Is(err, ErrListenTemplateNotFound) {
		t.Fatalf("error = %v", err)
	}
}
