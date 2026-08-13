package template

import (
	"fmt"
	"sync/atomic"

	"stressbot/admin/apierror"
)

type Error = apierror.Error

var (
	ErrActionTemplateNotFound   = apierror.ErrActionTemplateNotFound
	ErrListenTemplateNotFound   = apierror.ErrListenTemplateNotFound
	ErrTemplateNameConflict     = apierror.ErrTemplateNameConflict
	ErrTemplateSnapshotConflict = apierror.ErrTemplateSnapshotConflict
)

var testIDSequence atomic.Uint64

func testNextID() string {
	return fmt.Sprintf("%032x", testIDSequence.Add(1))
}
