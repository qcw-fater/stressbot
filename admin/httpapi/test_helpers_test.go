package httpapi

import (
	"database/sql"
	"fmt"
	"sync/atomic"

	"stressbot/admin/template"
)

var testIDSequence atomic.Uint64

func testNextID() string { return fmt.Sprintf("test-%d", testIDSequence.Add(1)) }

func newActionTemplateStore(db *sql.DB) *template.ActionTemplateStore {
	return template.NewActionTemplateStore(db, testNextID)
}

func newListenTemplateStore(db *sql.DB) *template.ListenTemplateStore {
	return template.NewListenTemplateStore(db, testNextID)
}

func newFlowTemplateStore(db *sql.DB) *template.FlowTemplateStore {
	return template.NewFlowTemplateStore(db, testNextID)
}
