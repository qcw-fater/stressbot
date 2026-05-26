package network

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"stressbot/adapter"
	"stressbot/monitor"
	"stressbot/utils"
	stresslog "stressbot/utils/log"

	"github.com/panjf2000/gnet/v2"
	"go.uber.org/zap"
)

// maxBodyLen 单个包体最大允许长度（16MB），防止畸形/恶意包导致 OOM。
const maxBodyLen = 16 * 1024 * 1024

const (
	gnetReadBufferCap  = 64 * 1024 // gnet 读缓冲区容量
	gnetWriteBufferCap = 64 * 1024 // gnet 写缓冲区容量
)

// connRegistry 管理 gnet 连接与业务层 Connection 的映射。
type connRegistry struct {
	mu      sync.RWMutex
	connMap map[int]*Connection
}

func newConnRegistry() *connRegistry {
	return &connRegistry{
		connMap: make(map[int]*Connection),
	}
}

func (r *connRegistry) register(gconn gnet.Conn, conn *Connection) {
	r.mu.Lock()
	r.connMap[gconn.Fd()] = conn
	r.mu.Unlock()
}

func (r *connRegistry) unregister(gconn gnet.Conn) {
	r.mu.Lock()
	delete(r.connMap, gconn.Fd())
	r.mu.Unlock()
}

func (r *connRegistry) get(gconn gnet.Conn) *Connection {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.connMap[gconn.Fd()]
}

// EventServer gnet 事件处理器。
type EventServer struct {
	gnet.BuiltinEventEngine

	registry     *connRegistry
	adp          adapter.Adapter
	tickInterval time.Duration
}

// Compile-time assertion: EventServer satisfies gnet.EventHandler.
var _ gnet.EventHandler = (*EventServer)(nil)

// NewEventServer 创建 gnet 事件处理器
func NewEventServer(adp adapter.Adapter, heartbeatInterval time.Duration) *EventServer {
	return &EventServer{
		registry:     newConnRegistry(),
		adp:          adp,
		tickInterval: heartbeatInterval,
	}
}

// OnOpen gnet 连接建立回调
func (es *EventServer) OnOpen(gconn gnet.Conn) ([]byte, gnet.Action) {
	return nil, gnet.None
}

// OnClose gnet 连接关闭回调。
//
// 把 err 字符串归一化为简短 reason 传给 Connection.onClose，让 inflight RequestResponse
// 命中 ctx.Done() 时能拼到错误 detail（例如 "cause=EOF" / "cause=RST(forcibly closed)"）。
// reason="" 表示无错误的正常关闭（服务端调 close 但底层没报 error，少见）。
func (es *EventServer) OnClose(gconn gnet.Conn, err error) gnet.Action {
	conn := es.registry.get(gconn)
	if conn != nil {
		es.registry.unregister(gconn)
		if err != nil {
			stresslog.Warn("[GNET] 连接异常关闭",
				zap.String("service", conn.ServiceName()), zap.String("robot", conn.robotName), zap.Error(err))
		} else {
			stresslog.Debug("[GNET] 连接正常关闭",
				zap.String("service", conn.ServiceName()), zap.String("robot", conn.robotName))
		}
		conn.onClose(closeReasonFromErr(err))
	}
	return gnet.None
}

// closeReasonFromErr 把 gnet 给的 close error 归一化为短标签字符串。
//
// 设计：保留辨识度但去除冗长的地址/系统调用名，让前端面板能直接展示；
// 同一类断开归一为同一个标签便于聚合（避免每个连接的 IP:port 撑出唯一字符串
// 爆炸聚合维度，monitor 用 (Kind,Code,Detail) 做错误聚合）。
func closeReasonFromErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	switch {
	case s == "EOF":
		return "EOF"
	case strings.Contains(s, "forcibly closed"), strings.Contains(s, "connection reset"):
		return "RST"
	case strings.Contains(s, "broken pipe"):
		return "broken-pipe"
	case strings.Contains(s, "use of closed network connection"):
		return "local-close"
	case strings.Contains(s, "timeout"), strings.Contains(s, "deadline"):
		return "timeout"
	default:
		// 兜底：保留原错误但截断防止 detail 过长污染聚合维度
		if len(s) > 64 {
			s = s[:64]
		}
		return s
	}
}

// OnTraffic gnet 收到数据回调。
//
// 设计原则：此函数运行在 gnet 事件循环 goroutine 上，**严禁同步阻塞**
// （阻塞会让该 loop 上所有连接的 I/O 一起冻结，是 CONN_DROPPED 雪崩的源头）。
//
// 因此只做两件纯 Go 操作：
//  1. 用 adapter 缓存的元信息做帧分割（HeaderSize / BodyLength，零 Lua 调用）
//  2. 把 raw msgBuf 投递到 connection 的 decodeCh，由 per-connection 的
//     decodeLoop goroutine 异步完成 Lua decode + 分发
//
// msgBuf 从 sync.Pool 获取，decodeLoop 处理完归还，避免高频 alloc 触发 GC 抖动。
func (es *EventServer) OnTraffic(gconn gnet.Conn) (action gnet.Action) {
	headSize := es.adp.HeaderSize()

	conn := es.registry.get(gconn)

	for {
		available := gconn.InboundBuffered()
		if available < headSize {
			return gnet.None
		}

		headBuf, err := gconn.Peek(headSize)
		if err != nil || len(headBuf) < headSize {
			return gnet.None
		}

		bodyLen := es.adp.BodyLength(headBuf)
		if bodyLen < 0 || bodyLen > maxBodyLen {
			serviceName := ""
			if conn != nil {
				serviceName = conn.ServiceName()
			}
			stresslog.Warn("[NETWORK] 协议头非法或包体过长，关闭连接",
				zap.String("service", serviceName),
				zap.Int("bodyLen", bodyLen))
			return gnet.Close
		}

		totalLen := headSize + bodyLen
		if available < totalLen {
			return gnet.None
		}

		msgBuf := getMsgBuf(totalLen)
		if _, err = gconn.Read(msgBuf); err != nil {
			putMsgBuf(msgBuf)
			stresslog.Error("[GNET] 读取消息失败", zap.Error(err))
			return gnet.None
		}
		// 全局带宽统计：所有真实入站字节都计入（含心跳应答、监听推送、未匹配响应等）。
		// 与 connection.Send 的出站统计配对，monitor 拿到的是"网卡级"双向流量。
		monitor.Global().AddBandwidth(0, int64(totalLen))

		if conn == nil {
			putMsgBuf(msgBuf)
			stresslog.Warn("[NETWORK] 收到消息但连接未注册，消息被丢弃",
				zap.Int("fd", gconn.Fd()), zap.Int("bodyLen", totalLen))
			continue
		}

		switch conn.EnqueueRaw(msgBuf) {
		case EnqueueOK:
			// 入队成功，msgBuf 由 decodeLoop 在处理后归还
		case EnqueueClosed:
			// 连接已关闭或还没启动 decode：这是正常现象（任务停止 / battle_end close_* /
			// 服务端 EOF 后 inbound 字节仍在路上）。归还 buffer 即可，不重复关闭、不报警。
			putMsgBuf(msgBuf)
			stresslog.Debug("[NETWORK] 连接已关闭，丢弃后续 inbound 帧",
				zap.String("service", conn.ServiceName()),
				zap.String("robot", conn.robotName),
				zap.Int("bodyLen", totalLen))
		case EnqueueChFull:
			// decodeCh 真满 = decode 严重落后（Lua 池耗尽或对端发包速率超出处理能力）。
			// 关闭这条连接释放资源，避免持续累积导致整体雪崩。
			putMsgBuf(msgBuf)
			stresslog.Warn("[NETWORK] decode 通道已满，关闭连接以释放压力",
				zap.String("service", conn.ServiceName()),
				zap.String("robot", conn.robotName))
			return gnet.Close
		}
	}
}

// OnTick gnet 定时回调。
func (es *EventServer) OnTick() (delay time.Duration, action gnet.Action) {
	return es.tickInterval, gnet.None
}

// bindConn 将 gnet 连接的发送/关闭函数注入到业务层 Connection。
func bindConn(gconn gnet.Conn, conn *Connection) {
	conn.sendFunc = func(data []byte) error {
		return gconn.AsyncWrite(data, nil)
	}
	conn.closeFunc = func() error {
		return gconn.Close()
	}
}

// Dialer 管理 gnet 客户端。
type Dialer struct {
	client *gnet.Client
	server *EventServer
}

// NewDialer 创建拨号器
func NewDialer(adp adapter.Adapter, heartbeatInterval time.Duration) *Dialer {
	server := NewEventServer(adp, heartbeatInterval)
	return &Dialer{
		server: server,
	}
}

// Start 启动 gnet 客户端引擎
func (d *Dialer) Start() error {
	opts := []gnet.Option{
		gnet.WithTicker(true),
		gnet.WithReadBufferCap(gnetReadBufferCap),
		gnet.WithWriteBufferCap(gnetWriteBufferCap),
	}
	client, err := gnet.NewClient(d.server, opts...)
	if err != nil {
		return fmt.Errorf("创建 gnet 客户端失败: %w", err)
	}
	d.client = client

	if err = d.client.Start(); err != nil {
		return fmt.Errorf("启动 gnet 客户端失败: %w", err)
	}

	stresslog.Info("[GNET] 客户端引擎已启动")
	return nil
}

// Stop 停止 gnet 客户端引擎
func (d *Dialer) Stop() error {
	if d.client == nil {
		return nil
	}
	if err := d.client.Stop(); err != nil {
		return fmt.Errorf("停止 gnet 客户端失败: %w", err)
	}
	stresslog.Info("[GNET] 客户端引擎已停止")
	return nil
}

// DialTCP 建立 TCP 连接并绑定业务层 Connection。
// ctx 用于超时/取消：如果 ctx 在拨号完成前被取消，返回 context 错误。
func (d *Dialer) DialTCP(ctx context.Context, address string, conn *Connection) (gnet.Conn, error) {
	type dialResult struct {
		conn gnet.Conn
		err  error
	}
	ch := make(chan dialResult, 1)
	utils.GetWorkPool().Go(func() {
		gc, e := d.client.Dial("tcp", address)
		ch <- dialResult{gc, e}
	})

	select {
	case <-ctx.Done():
		// ctx 取消，拨号可能已完成，需排空结果并关闭 gnet 连接避免 fd 泄漏
		utils.GetWorkPool().Go(func() {
			if res := <-ch; res.conn != nil {
				_ = res.conn.Close()
			}
		})
		return nil, ctx.Err()
	case res := <-ch:
		if res.err != nil {
			return nil, fmt.Errorf("TCP 拨号失败 %s: %w", address, res.err)
		}
		gconn := res.conn
		bindConn(gconn, conn)
		d.server.registry.register(gconn, conn)
		// 启动异步 decode goroutine：必须在 register 之后立即启动，
		// 否则首批 OnTraffic 到达时 decodeCh 还没准备好，会被 EnqueueRaw 拒绝。
		conn.StartDecodeLoop(d.server.adp, false)

		stresslog.Info("[GNET] TCP 连接已建立",
			zap.String("address", address), zap.String("service", conn.serviceName), zap.String("robot", conn.robotName))
		return gconn, nil
	}
}

// DialUDP 建立 UDP 连接并绑定业务层 Connection。
func (d *Dialer) DialUDP(address string, conn *Connection) (gnet.Conn, error) {
	gconn, err := d.client.Dial("udp", address)
	if err != nil {
		return nil, fmt.Errorf("UDP 拨号失败 %s: %w", address, err)
	}

	bindConn(gconn, conn)
	d.server.registry.register(gconn, conn)
	conn.StartDecodeLoop(d.server.adp, true)

	stresslog.Info("[GNET] UDP 连接已建立", zap.String("address", address), zap.String("robot", conn.robotName))
	return gconn, nil
}
