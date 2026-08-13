package httpapi

import (
	"database/sql"
	"fmt"
	"sync/atomic"

	admintask "stressbot/admin/task"
	"stressbot/admin/template"
)

var testIDSequence atomic.Uint64

func testNextID() string { return fmt.Sprintf("test-%d", testIDSequence.Add(1)) }

var NewTaskStore = admintask.NewTaskStore

func NewActionTemplateStore(db *sql.DB) *template.ActionTemplateStore {
	return template.NewActionTemplateStore(db, testNextID)
}

func NewListenTemplateStore(db *sql.DB) *template.ListenTemplateStore {
	return template.NewListenTemplateStore(db, testNextID)
}

func NewFlowTemplateStore(db *sql.DB) *template.FlowTemplateStore {
	return template.NewFlowTemplateStore(db, testNextID)
}
