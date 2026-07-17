---
name: go-design-patterns
description: Use when 需要实现 Go 设计模式、询问代码架构方案或审查 Go 代码结构时；示例必须使用通用业务域，禁止引入 Player/Room/Battle 等游戏服务器专用概念。
---

# Go 设计模式技能

## 核心原则

- **优先组合而非继承**：Go 没有继承，通过接口 + 结构体嵌入实现
- **接口小而专注**：1-3 个方法的接口才是 Go 风格
- **简单优于复杂**：不要为了套模式而套模式，三行能解决的不用模式
- **并发安全优先**：涉及共享状态的模式必须加锁或用 channel

---

## 创建型模式

### 1. 工厂方法 (Factory Method)

**场景**：根据参数创建不同类型对象；创建逻辑复杂需要封装。

```go
type MessageHandler interface {
    Handle(data []byte) error
}

func NewMessageHandler(kind string) (MessageHandler, error) {
    switch kind {
    case "http":
        return &HTTPHandler{}, nil
    case "queue":
        return &QueueHandler{}, nil
    default:
        return nil, fmt.Errorf("unknown handler kind: %s", kind)
    }
}
```

> 工厂函数返回接口而非具体类型；未知类型返回 `error` 而非 `nil`。

---

### 2. 单例模式 (Singleton)

**场景**：全局唯一实例（配置管理器、连接池、服务注册表）。

```go
var (
    registry     *ResourceRegistry
    registryOnce sync.Once
)

func GetResourceRegistry() *ResourceRegistry {
    registryOnce.Do(func() {
        registry = &ResourceRegistry{
            resources: make(map[int64]*Resource),
        }
    })
    return registry
}
```

> 使用 `sync.Once` 而非 `init()` 或 double-check locking；单例难以替换，测试时考虑依赖注入。

---

### 3. 函数选项 (Functional Options) ⭐ 推荐

**场景**：创建有多个可选参数的复杂对象；可扩展而不破坏兼容性。

```go
type ClientOption func(*ClientConfig)

func WithMaxConns(n int) ClientOption {
    return func(c *ClientConfig) { c.maxConns = n }
}

func WithTimeout(d time.Duration) ClientOption {
    return func(c *ClientConfig) { c.timeout = d }
}

func NewClient(opts ...ClientOption) *Client {
    cfg := &ClientConfig{maxConns: 4, timeout: 30 * time.Second}
    for _, opt := range opts {
        opt(cfg)
    }
    return &Client{config: cfg}
}

// 使用
client := NewClient(WithMaxConns(6), WithTimeout(60*time.Second))
```

> Go 中函数选项比 Builder 模式更惯用，优先使用函数选项。

---

### 4. 对象池 (Object Pool)

**场景**：对象创建成本高（大型 Protobuf Buffer）；需要频繁创建销毁以减少 GC 压力。

```go
var bufferPool = sync.Pool{
    New: func() any {
        return new(bytes.Buffer)
    },
}

func GetBuffer() *bytes.Buffer {
    b := bufferPool.Get().(*bytes.Buffer)
    b.Reset()
    return b
}

func PutBuffer(b *bytes.Buffer) { bufferPool.Put(b) }
```

> `sync.Pool` 内对象可能被 GC 回收，不适合持久连接池；归还前必须清理敏感数据。

---

## 结构型模式

### 5. 装饰器 (Decorator)

**场景**：动态为对象添加功能（日志、鉴权、限流），不修改原有代码。

```go
type DataService interface {
    Get(id int64) (*Record, error)
}

// 日志装饰器
type LoggingDataService struct{ inner DataService }

func (s *LoggingDataService) Get(id int64) (*Record, error) {
    start := time.Now()
    r, err := s.inner.Get(id)
    logger.Info("Get",
        zap.Int64("id", id),
        zap.Duration("cost", time.Since(start)),
        zap.Error(err))
    return r, err
}
```

> 装饰器依赖接口，可以叠加多层（日志 → 缓存 → 限流），顺序即是执行顺序。

---

### 6. 中间件链 (Middleware) ⭐

**场景**：请求处理管道（鉴权 → 限流 → 业务逻辑）。与 Pipeline 的区别：中间件共享同一个 context，Pipeline 通过 channel 传递数据，适合不同阶段有数据变换的流水线。

```go
type HandlerFunc func(ctx *RequestContext) error
type Middleware func(HandlerFunc) HandlerFunc

func AuthMiddleware(next HandlerFunc) HandlerFunc {
    return func(ctx *RequestContext) error {
        if ctx.User == nil {
            return ErrUnauthorized
        }
        return next(ctx)
    }
}

// 链式组合（从右到左包装，执行顺序从左到右）
func Chain(h HandlerFunc, middlewares ...Middleware) HandlerFunc {
    for i := len(middlewares) - 1; i >= 0; i-- {
        h = middlewares[i](h)
    }
    return h
}
```

---

### 7. 代理 (Proxy)

**场景**：延迟加载、访问控制、远程代理（RPC 客户端封装）。

```go
type LazyResourceProxy struct {
    id       int64
    resource *Resource
    once     sync.Once
    loader   func(int64) (*Resource, error)
}

func (p *LazyResourceProxy) Get() (*Resource, error) {
    var err error
    p.once.Do(func() { p.resource, err = p.loader(p.id) })
    return p.resource, err
}
```

---

## 行为型模式

### 8. 状态机 (State Machine) ⭐

**场景**：任务状态（等待→运行中→完成）；订单流程；连接生命周期；工作流状态管理。

```go
type JobState int32

const (
    JobStatePending   JobState = 0
    JobStateRunning   JobState = 1
    JobStateFinishing JobState = 2
    JobStateDone      JobState = 3
)

var validTransitions = map[JobState][]JobState{
    JobStatePending:   {JobStateRunning, JobStateDone},
    JobStateRunning:   {JobStateFinishing},
    JobStateFinishing: {JobStateDone},
    JobStateDone:      {},
}

func (j *Job) Transition(to JobState) error {
    j.mu.Lock()
    defer j.mu.Unlock()
    for _, valid := range validTransitions[j.state] {
        if valid == to {
            j.state = to
            return nil
        }
    }
    return fmt.Errorf("invalid transition: %d -> %d", j.state, to)
}
```

---

### 9. 策略 (Strategy)

**场景**：运行时切换算法（路由策略、排序策略、限流策略、重试策略）。

```go
type RoutingStrategy interface {
    Route(req *Request) string
}

type Router struct{ strategy RoutingStrategy }

func (r *Router) SetStrategy(s RoutingStrategy) { r.strategy = s }
func (r *Router) Route(req *Request) string {
    return r.strategy.Route(req)
}
```

---

### 10. 观察者 / 事件总线 (Observer / Event Bus)

**场景**：领域事件通知多个系统（缓存、统计、日志）。进程内同步通知用观察者，跨服务或需要缓冲时改用带 channel 的事件总线（异步）。

```go
type DomainEvent struct {
    EntityID int64
    Type     string
    Data     any
}

type EventObserver interface {
    OnEvent(event DomainEvent)
}

type EventBus struct {
    observers []EventObserver
    mu        sync.RWMutex
}

func (b *EventBus) Register(o EventObserver) {
    b.mu.Lock()
    defer b.mu.Unlock()
    b.observers = append(b.observers, o)
}

func (b *EventBus) Emit(event DomainEvent) {
    b.mu.RLock()
    observers := make([]EventObserver, len(b.observers))
    copy(observers, b.observers)
    b.mu.RUnlock()
    for _, o := range observers {
        o.OnEvent(event) // 异步改为：go o.OnEvent(event)
    }
}
```

---

## 并发模式

### 11. 有界并行 (Bounded Parallelism) ⭐

**场景**：批量处理任务或记录；限制并发数避免资源耗尽。

```go
func BatchProcessItems(ids []int64, maxWorkers int, process func(int64) error) {
    sem := make(chan struct{}, maxWorkers)
    var wg sync.WaitGroup
    for _, id := range ids {
        wg.Add(1)
        go func(itemID int64) {
            defer wg.Done()
            sem <- struct{}{}
            defer func() { <-sem }()
            if err := process(itemID); err != nil {
                logger.Error("batch item failed", zap.Int64("id", itemID), zap.Error(err))
            }
        }(id)
    }
    wg.Wait()
}
```

---

### 12. 断路器 (Circuit Breaker)

**场景**：防止级联故障（RPC 调用下游服务失败时快速返回）。

```go
type CBState int

const (
    CBClosed   CBState = iota // 正常：请求通过
    CBOpen                    // 断开：快速失败
    CBHalfOpen                // 半开：试探恢复
)

type CircuitBreaker struct {
    maxFailures int
    resetAfter  time.Duration
    failures    int
    lastFail    time.Time
    state       CBState
    mu          sync.Mutex
}

func (cb *CircuitBreaker) Call(fn func() error) error {
    cb.mu.Lock()
    if cb.state == CBOpen {
        if time.Since(cb.lastFail) > cb.resetAfter {
            cb.state = CBHalfOpen
        } else {
            cb.mu.Unlock()
            return fmt.Errorf("circuit breaker open")
        }
    }
    cb.mu.Unlock()

    err := fn()

    cb.mu.Lock()
    defer cb.mu.Unlock()
    if err != nil {
        cb.failures++
        cb.lastFail = time.Now()
        if cb.failures >= cb.maxFailures {
            cb.state = CBOpen
        }
        return err
    }
    cb.failures = 0
    cb.state = CBClosed
    return nil
}
```

---

## 反模式（不要做的事）

| 反模式 | 问题 | 正确做法 |
|--------|------|----------|
| 过度使用单例 | 全局状态导致测试困难 | 依赖注入，通过参数传递 |
| 空接口滥用 | `interface{}` 丢失类型信息 | 使用泛型或具体类型 |
| 为模式而模式 | 三行能写完的逻辑套了 5 个类 | 先写简单代码，需要时再重构 |
| goroutine 泄漏 | 启动 goroutine 不管退出 | 总是配合 `context` 或 `chan struct{}` |
| 锁内调用外部函数 | 死锁风险 | 锁的范围尽量小，只保护数据访问 |

---

## 快速选择表

| 问题场景 | 推荐模式 |
|----------|----------|
| 根据参数创建不同对象 | 工厂方法 |
| 全局唯一实例 | 单例 |
| 多可选参数创建对象 | 函数选项 ⭐ |
| 高创建成本对象复用 | 对象池 (sync.Pool) |
| 动态添加功能（日志/限流） | 装饰器 |
| 请求处理管道（共享 context） | 中间件链 ⭐ |
| 任务/工作流状态管理 | 状态机 ⭐ |
| 运行时切换算法 | 策略 |
| 一对多事件通知 | 观察者 / 事件总线 |
| 批量任务限制并发 | 有界并行 ⭐ |
| 防止下游级联故障 | 断路器 |
