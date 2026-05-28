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

func luaGzipDecompress(L *lua.LState) int {
	r, err := gzip.NewReader(bytes.NewReader([]byte(L.CheckString(1))))
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}
	defer r.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}
	L.Push(lua.LString(string(out)))
	return 1
}
