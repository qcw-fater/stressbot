// Package codec — 压缩算法实现（迁移自 adapter/lua_zlib.go）。
//
// 本层只做无阈值 gzip——压缩触发阈值由 engine 的 `when` 决定，本层不感知。
// gzip 实现（含 sync.Pool 复用 Writer/Reader）逐字迁移自 lua_zlib.go，仅去掉 Lua
// 入口包装，改为 Compressor 接口的 Go 方法。
//
// 迁移来源行号：
//   - gzipWriterPool / luaGzipCompress: lua_zlib.go:25/31
//   - gzipReaderPool / luaGzipDecompress: lua_zlib.go:59/61
package codec

import (
	"bytes"
	"compress/gzip"
	"io"
	"sync"
)

// gzipWriterPool 复用 *gzip.Writer（迁移自 lua_zlib.go:25）。
var gzipWriterPool = sync.Pool{
	New: func() any {
		return gzip.NewWriter(io.Discard)
	},
}

// gzipReaderPool 复用 *gzip.Reader（迁移自 lua_zlib.go:59）。
var gzipReaderPool = sync.Pool{}

// noneCompressor 直通。
type noneCompressor struct{}

func (noneCompressor) Compress(data []byte) ([]byte, error) {
	out := make([]byte, len(data))
	copy(out, data)
	return out, nil
}

func (noneCompressor) Decompress(data []byte) ([]byte, error) {
	out := make([]byte, len(data))
	copy(out, data)
	return out, nil
}

// gzipCompressor gzip 格式（RFC 1952）。与服务器协议一致。
type gzipCompressor struct{}

func (gzipCompressor) Compress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzipWriterPool.Get().(*gzip.Writer)
	w.Reset(&buf)

	_, writeErr := w.Write(data)
	closeErr := w.Close()
	w.Reset(io.Discard)
	gzipWriterPool.Put(w)

	if writeErr != nil {
		return nil, writeErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return buf.Bytes(), nil
}

func (gzipCompressor) Decompress(data []byte) ([]byte, error) {
	src := bytes.NewReader(data)

	var r *gzip.Reader
	if v := gzipReaderPool.Get(); v != nil {
		r = v.(*gzip.Reader)
		if err := r.Reset(src); err != nil {
			// Reset 失败时该 reader 已不可用，丢弃不归还。
			return nil, err
		}
	} else {
		var err error
		r, err = gzip.NewReader(src)
		if err != nil {
			return nil, err
		}
	}

	out, err := io.ReadAll(r)
	closeErr := r.Close()
	// 仅在读与关闭均成功时归还 reader；任一失败则丢弃（reader 状态不确定）。
	// 与原 lua_zlib.go 行为对齐（原实现总是归还），但更稳妥。
	if err == nil && closeErr == nil {
		gzipReaderPool.Put(r)
	}
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return out, nil
}

func init() {
	RegisterCompressor("none", noneCompressor{}, AlgoMeta{
		Description: "直通（不压缩）",
	})
	RegisterCompressor("gzip", gzipCompressor{}, AlgoMeta{
		Description: "gzip 格式（RFC 1952），无损往返；压缩阈值由 engine `when` 控制，本层无阈值",
	})
}
