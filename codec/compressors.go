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
	"encoding/binary"
	"errors"
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

	out, err := readAllSized(r, gzipSizeHint(data))
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

// gzipSizeHintMax 尺寸提示上限：防止损坏的 trailer 声称超大长度导致一次性巨额分配。
// 超过上限时按上限分配，真实数据更长由 readAllSized 的追加路径兜底（语义不变）。
const gzipSizeHintMax = 16 << 20

// gzipSizeHint 从 gzip trailer 的 ISIZE 字段（末 4 字节小端 = 解压后长度 mod 2^32）
// 读取输出尺寸提示。单成员流（现协议帧）下即精确长度；多成员/损坏 trailer 时只是
// 偏差的提示，readAllSized 两端都能兜（短则截断、长则追加），不影响正确性。
func gzipSizeHint(data []byte) int {
	if len(data) >= 4 {
		if n := binary.LittleEndian.Uint32(data[len(data)-4:]); n > 0 {
			if n > gzipSizeHintMax {
				return gzipSizeHintMax
			}
			return int(n)
		}
	}
	return len(data) * 3
}

// readAllSized 按尺寸提示一次分配读满 r（io.ReadAll 的定长替代）。
// 提示精确时恰好一次分配零拷贝迁移；提示偏短时退化为追加（等价 ReadAll）；
// 提示偏长时截断到实际长度。收包热路径上替代 ReadAll 的 512B 起步倍增扩容
//（大帧一次解压要经历多轮 growslice + 复制，剖面周期分配 36GB 的主源）。
func readAllSized(r io.Reader, hint int) ([]byte, error) {
	if hint < 64 {
		hint = 64
	}
	out := make([]byte, hint)
	n, err := io.ReadFull(r, out)
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return out[:n], nil // 实际比提示短：截断即全部数据
	}
	if err != nil {
		return nil, err
	}
	// 读满提示长度：单字节探针判定是否恰好 EOF（精确提示的常规路径，零额外分配）。
	var one [1]byte
	m, perr := r.Read(one[:])
	if m > 0 {
		out = append(out, one[0])
		rest, rerr := io.ReadAll(r)
		out = append(out, rest...)
		perr = rerr
	} else if perr == nil {
		// (0, nil) 是合法的 io.Reader 返回：不可据此判定 EOF，交给 ReadAll 收尾。
		rest, rerr := io.ReadAll(r)
		out = append(out, rest...)
		perr = rerr
	}
	if perr != nil && perr != io.EOF {
		return nil, perr
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
