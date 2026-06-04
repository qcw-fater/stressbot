package adapter

import (
	"bytes"
	"compress/gzip"
	"io"
	"sync"

	lua "github.com/yuin/gopher-lua"
)

// RegisterZlibModule 向 LState 预加载 zlib Lua 模块。
// codec.lua 通过 local zlib = require("zlib") 加载。
// 使用 gzip 格式（RFC 1952）与服务器协议一致。
func RegisterZlibModule(L *lua.LState) {
	L.PreloadModule("zlib", func(L *lua.LState) int {
		mod := L.NewTable()
		L.SetField(mod, "compress", L.NewFunction(luaGzipCompress))
		L.SetField(mod, "decompress", L.NewFunction(luaGzipDecompress))
		L.Push(mod)
		return 1
	})
}

var gzipWriterPool = sync.Pool{
	New: func() any {
		return gzip.NewWriter(io.Discard)
	},
}

func luaGzipCompress(L *lua.LState) int {
	data := []byte(L.CheckString(1))
	var buf bytes.Buffer
	w := gzipWriterPool.Get().(*gzip.Writer)
	w.Reset(&buf)

	_, writeErr := w.Write(data)
	closeErr := w.Close()
	w.Reset(io.Discard)
	gzipWriterPool.Put(w)

	if writeErr != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(writeErr.Error()))
		return 2
	}
	if closeErr != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(closeErr.Error()))
		return 2
	}
	L.Push(lua.LString(buf.String()))
	return 1
}

// gzipReaderPool 复用 gzip.Reader，避免每次解压都新建 flate.Reader 与 32KB 字典窗
// （pprof 显示 flate.NewReader + dictDecoder.init 占用了可观的分配 churn）。
// 池中存的是 *gzip.Reader 指针，通过 Reset 重新绑定输入流。
var gzipReaderPool = sync.Pool{}

func luaGzipDecompress(L *lua.LState) int {
	src := bytes.NewReader([]byte(L.CheckString(1)))

	var r *gzip.Reader
	if v := gzipReaderPool.Get(); v != nil {
		r = v.(*gzip.Reader)
		if err := r.Reset(src); err != nil {
			// Reset 失败时该 reader 已不可用，丢弃不归还。
			L.Push(lua.LNil)
			L.Push(lua.LString(err.Error()))
			return 2
		}
	} else {
		var err error
		r, err = gzip.NewReader(src)
		if err != nil {
			L.Push(lua.LNil)
			L.Push(lua.LString(err.Error()))
			return 2
		}
	}

	out, err := io.ReadAll(r)
	closeErr := r.Close()
	gzipReaderPool.Put(r)

	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}
	if closeErr != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(closeErr.Error()))
		return 2
	}
	L.Push(lua.LString(string(out)))
	return 1
}
