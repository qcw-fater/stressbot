package logview

import (
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap/zapcore"
)

// Entry 单条结构化日志。
type Entry struct {
	Level   string  `json:"level"`
	Time    time.Time `json:"time"`
	Caller  string  `json:"caller,omitempty"`
	Message string  `json:"message"`
	Service string  `json:"service,omitempty"`
	Fields  []Field `json:"fields,omitempty"`
}

// Field 序列化后的 zap 字段键值对。
type Field struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// QueryParams 查询参数。
type QueryParams struct {
	AfterSeq uint64 // 游标：仅返回 seq > AfterSeq 的条目
	Limit    int    // 最大返回条数
}

// QueryResult 查询结果。
type QueryResult struct {
	Entries []Entry `json:"entries"`
	HasMore bool    `json:"hasMore"`
	NextSeq uint64  `json:"nextSeq"`
}

type entryWithSeq struct {
	level   string
	time    time.Time
	caller  string
	message string
	service string
	fields  []zapcore.Field
	seq     uint64
}

func (e *entryWithSeq) toEntry() Entry {
	return Entry{
		Level:   e.level,
		Time:    e.time,
		Caller:  e.caller,
		Message: e.message,
		Service: e.service,
		Fields:  fieldsToFields(e.fields),
	}
}

// RingBuffer 线程安全的固定大小环形缓冲区。
type RingBuffer struct {
	mu    sync.RWMutex
	buf   []entryWithSeq
	size  int
	head  int
	count int
	seq   atomic.Uint64
}

// NewRingBuffer 创建指定大小的环形缓冲区。
func NewRingBuffer(size int) *RingBuffer {
	return &RingBuffer{
		buf:  make([]entryWithSeq, size),
		size: size,
	}
}

// Append 写入一条日志（O(1)，Write Lock）。
func (rb *RingBuffer) Append(level string, t time.Time, caller, message, service string, fields []zapcore.Field) {
	s := rb.seq.Add(1)

	rb.mu.Lock()
	rb.buf[rb.head] = entryWithSeq{
		level:   level,
		time:    t,
		caller:  caller,
		message: message,
		service: service,
		fields:  fields,
		seq:     s,
	}
	rb.head = (rb.head + 1) % rb.size
	if rb.count < rb.size {
		rb.count++
	}
	rb.mu.Unlock()
}

// Query 按游标查询（Read Lock）。过滤由前端负责。
func (rb *RingBuffer) Query(params QueryParams) QueryResult {
	limit := params.Limit
	if limit <= 0 {
		limit = 200
	}

	rb.mu.RLock()
	count := rb.count
	if count == 0 {
		rb.mu.RUnlock()
		return QueryResult{Entries: []Entry{}, HasMore: false, NextSeq: 0}
	}

	start := (rb.head - count + rb.size) % rb.size
	buf := rb.buf
	size := rb.size
	rb.mu.RUnlock()

	var entries []Entry
	lastSeq := uint64(0)
	collected := 0
	hasMore := false

	for i := 0; i < count; i++ {
		idx := (start + i) % size
		item := &buf[idx]

		if item.seq <= params.AfterSeq {
			continue
		}

		collected++
		if collected > limit {
			hasMore = true
			break
		}
		entries = append(entries, item.toEntry())
		lastSeq = item.seq
	}

	if entries == nil {
		entries = []Entry{}
	}

	return QueryResult{
		Entries: entries,
		HasMore: hasMore,
		NextSeq: lastSeq,
	}
}

// ParseUint64OrDefault 解析 uint64，失败返回默认值。
func ParseUint64OrDefault(s string, def uint64) uint64 {
	if s == "" {
		return def
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return def
	}
	return v
}

// fieldsToFields 将 zapcore.Field 列表转为 []Field（仅在查询时调用）。
func fieldsToFields(fields []zapcore.Field) []Field {
	if len(fields) == 0 {
		return nil
	}
	out := make([]Field, 0, len(fields))
	for _, f := range fields {
		enc := zapcore.NewMapObjectEncoder()
		f.AddTo(enc)
		for k, v := range enc.Fields {
			out = append(out, Field{Key: k, Value: fmt.Sprintf("%v", v)})
		}
	}
	return out
}
