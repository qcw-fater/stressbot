package network

import "sync"

// bufSizeClasses 分级池容量。
// 按"≤大小→落入该桶"匹配，避免大包驱逐小包导致命中率劣化。
// 桶的最大尺寸覆盖了游戏协议常见的包大小区间，超大包不池化（频次极低）。
var bufSizeClasses = [...]int{256, 1024, 4096, 16384, 65536}

// msgBufPools 与 bufSizeClasses 一一对应的 sync.Pool。
// 池内存放 *[]byte 而非 []byte，避免归还时 slice header 逃逸到堆。
var msgBufPools [len(bufSizeClasses)]sync.Pool

func init() {
	for i := range bufSizeClasses {
		sz := bufSizeClasses[i]
		msgBufPools[i].New = func() any {
			b := make([]byte, sz)
			return &b
		}
	}
}

// getMsgBuf 从分级池获取容量 ≥ size 的字节切片，并切片到 size 长度。
// 返回的切片必须最终调用 putMsgBuf 归还（否则等同 make + GC，无优化也无泄漏）。
//
// 大于最大桶的请求直接 make，不入池——避免大包占用池内存影响命中率。
func getMsgBuf(size int) []byte {
	for i, classSize := range bufSizeClasses {
		if size <= classSize {
			bp := msgBufPools[i].Get().(*[]byte)
			return (*bp)[:size]
		}
	}
	return make([]byte, size)
}

// putMsgBuf 归还字节切片到对应分级池。
// 只接受 cap 严格等于某个桶尺寸的切片；其他直接丢弃由 GC 回收。
//
// 调用方传入的切片归还后不得再访问其底层数组。
func putMsgBuf(buf []byte) {
	if buf == nil {
		return
	}
	sz := cap(buf)
	for i, classSize := range bufSizeClasses {
		if sz == classSize {
			b := buf[:sz]
			msgBufPools[i].Put(&b)
			return
		}
	}
}
