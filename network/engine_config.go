package network

import "runtime"

// gnet 引擎调优参数的编译期默认值；EngineConfig 任一项 ≤ 0（含零值未配置）回退到这里。
const (
	defaultReadBufferCap  = 32 * 1024        // gnet 读缓冲区容量
	defaultWriteBufferCap = 32 * 1024        // gnet 写缓冲区容量
	defaultMaxDials       = 512              // 同一进程内同时阻塞在拨号阶段的连接数上限
	defaultMaxBodyLen     = 16 * 1024 * 1024 // 单个协议包最大 body 字节数（防畸形包 OOM）
)

// EngineConfig gnet 网络引擎调优参数，对应 TOML [network] 段（单机 stressbot.toml 与 agent.toml 均支持）。
// 一般无需修改，仅在万级以上并发或超大协议包场景下酌情调整；所有项留空/0 = 默认。
type EngineConfig struct {
	ReadBufferCap      int `toml:"readBufferCap"      json:"readBufferCap"`
	WriteBufferCap     int `toml:"writeBufferCap"     json:"writeBufferCap"`
	NumEventLoop       int `toml:"numEventLoop"       json:"numEventLoop"`
	MaxConcurrentDials int `toml:"maxConcurrentDials" json:"maxConcurrentDials"`
	MaxBodyLen         int `toml:"maxBodyLen"         json:"maxBodyLen"`
}

// engineParams 归一化后的引擎参数（全部为正值，Dialer / EventServer 直接消费）。
type engineParams struct {
	readBufferCap      int
	writeBufferCap     int
	numEventLoop       int
	maxConcurrentDials int
	maxBodyLen         int
}

// normalize 把配置归一化为实际参数：任一项 ≤ 0 视为未配置，回退编译期默认；
// numEventLoop 的默认是 runtime.NumCPU()（多 eventloop 让不同连接的回调真正并发）。
func (c EngineConfig) normalize() engineParams {
	return engineParams{
		readBufferCap:      engineOrDefault(c.ReadBufferCap, defaultReadBufferCap),
		writeBufferCap:     engineOrDefault(c.WriteBufferCap, defaultWriteBufferCap),
		numEventLoop:       engineOrDefault(c.NumEventLoop, runtime.NumCPU()),
		maxConcurrentDials: engineOrDefault(c.MaxConcurrentDials, defaultMaxDials),
		maxBodyLen:         engineOrDefault(c.MaxBodyLen, defaultMaxBodyLen),
	}
}

func engineOrDefault(value, def int) int {
	if value > 0 {
		return value
	}
	return def
}
