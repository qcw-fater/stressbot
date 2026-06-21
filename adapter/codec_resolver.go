// Package adapter — CodecResolver：按「server 串 → Adapter」显式映射，无 fallback。
//
// 这是 Track 4.1 的基础设施，供 T2 在 runtime 接线（main.go/task_runner 调 LoadCodecResolver →
// resolver 传给 Manager，Robot/Dialer 按 <proto>:<service> 解析每连接的 Adapter）。
//
// 设计要点（总纲决策 #8 + T4.1 brief）：
//   - codec 与连接绑定（每连接一份 codec 文件），resolver 是显式 server 串 → Adapter 映射。
//   - **无 fallback**（遵循「禁止兼容性兜底」）：缺映射返回 nil，由调用方 fail loud；
//     loader 缺文件 / 解析失败 / 校验失败 / 空映射均返回中文 error，绝不静默回退默认 codec。
//   - 同文件 dedup：同一 codec 文件被多个 server 引用时，编译一次、共享同一无状态 Adapter 实例。
//   - errors.json 可选：传空字符串跳过错误码 map（Adapter 仍可用，DescribeError 返回空串）。
//   - resolver 构造后 byServer 只读（构造期填好后不再变），并发安全。
//
// InferCodecMap（T2-C1 新增）：扫 codecDir 下 *_codec.json → server 串，省去 main.go
// 手写 codecs map；文件名规约 `<proto>_<service>_codec.json` 与 T1.6 产物一致。
//
// 仅 import codec + stdlib（不 import gopher-lua）。loader 只做「加载 + 编译 + 组装 resolver」，
// 不读运行 config（config 解析 + 调 loader 是 T2 的 main.go/task_runner 职责）。
package adapter

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"stressbot/codec"
)

// codecFileSuffix 是 codec 配置文件名的固定后缀。
// 文件名规约：<proto>_<service>_codec.json（如 tcp_logic_codec.json）。
const codecFileSuffix = "_codec.json"

// InferCodecMap 扫描 codecDir 下的 *_codec.json 文件，推断出「server 串 → 文件名」映射。
//
// 文件名规约：`<proto>_<service>_codec.json` → server 串 `<proto>:<service>`。
// 例如 `tcp_logic_codec.json` → "tcp:logic"，`udp_battle_codec.json` → "udp:battle"。
//
// 行为：
//   - 仅匹配后缀 `_codec.json`（errors.json / codec.lua / error.lua / 其它文件不会被收）。
//   - 文件名去 `_codec.json` 后缀后，按**首个** `_` 拆 `<proto>` 与 `<service>`。
//     service 内部允许包含下划线（如 `tcp_rank_team_codec.json` → "tcp:rank_team"）。
//   - proto 或 service 为空（拆不出）→ 返回中文 error，带文件名（配置错误 fail loud）。
//   - codecDir 不存在 / 不可读 → 返回 error（os.ReadDir 失败透传）。
//   - 目录下无任何 `*_codec.json` → 返回中文 error（不静默返回空 map）。
//
// 返回的 map 直接可喂给 LoadCodecResolver（同 codecDir）。
// 映射顺序对行为无影响（LoadCodecResolver 内部按 server 串排序遍历）；这里为可读性按 server 串排序。
func InferCodecMap(codecDir string) (map[string]string, error) {
	entries, err := os.ReadDir(codecDir)
	if err != nil {
		return nil, fmt.Errorf("codec 目录 %q 读取失败：%w", codecDir, err)
	}

	m := make(map[string]string)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// 仅收 *_codec.json（排除 errors.json / 任意 .json）。
		if !strings.HasSuffix(name, codecFileSuffix) {
			continue
		}
		// 去后缀得到 <proto>_<service>。
		stem := strings.TrimSuffix(name, codecFileSuffix)
		// 按首个 `_` 拆 proto / service。
		idx := strings.Index(stem, "_")
		if idx <= 0 || idx == len(stem)-1 {
			// 没下划线 / 首字符即下划线 / 末字符即下划线 → 无法拆出 proto:service。
			return nil, fmt.Errorf("codec 文件名 %q 无法解析为 <proto>_<service>%s", name, codecFileSuffix)
		}
		proto := stem[:idx]
		service := stem[idx+1:]
		server := proto + ":" + service
		// 同名文件理论上 os.ReadDir 不会重复（目录项唯一）；保留覆盖以防御意外。
		m[server] = name
	}

	if len(m) == 0 {
		return nil, fmt.Errorf("codec 目录 %q 下无任何 *%s 文件（未声明任何连接的 codec）", codecDir, codecFileSuffix)
	}
	return m, nil
}

// CodecResolver：server 串（"<proto>:<service>"，如 "tcp:logic"/"tcp:battle"/"udp:battle"）→ Adapter。
// 缺映射返回 nil，由调用方 fail loud（不在 resolver 内 panic）。
type CodecResolver interface {
	Resolve(server string) Adapter
}

// codecResolver：纯显式映射，无 fallback（遵循「禁止兼容性兜底」）。
// 构造后 byServer 只读；并发 Resolve 无需加锁。
type codecResolver struct {
	byServer map[string]Adapter
}

// 编译期断言：*codecResolver 必须实现 CodecResolver 接口。
var _ CodecResolver = (*codecResolver)(nil)

// Resolve 返回 server 串对应的 Adapter；未声明返回 nil。
func (r *codecResolver) Resolve(server string) Adapter { return r.byServer[server] }

// NewCodecResolver：显式 map 构造（每个声明的 server → 其 codec）。
// 入参 byServer 可为空（构造不校验，与 loader 不同——loader 对空映射 fail loud，但
// 直接构造允许上层传入已组装好的映射，含空集）。
func NewCodecResolver(byServer map[string]Adapter) CodecResolver {
	// 防御性拷贝：避免上层后续修改影响 resolver 内部状态（resolver 构造后应只读）。
	copied := make(map[string]Adapter, len(byServer))
	for k, v := range byServer {
		copied[k] = v
	}
	return &codecResolver{byServer: copied}
}

// LoadCodecResolver 按「server 串 → codec 文件名」映射，从 codecDir 逐份加载并构建 resolver。
//
// 入参：
//   - codecDir   : 存放 *_codec.json 的目录（如 "conf/adapter"）。
//   - codecs     : server串 → 文件名（如 "tcp:logic" → "tcp_logic_codec.json"）。
//   - errorsFile : 共享 errors.json 的路径（相对 codecDir 或绝对；为空则不加载错误码 map）。
//
// 行为：
//   - 同一文件被多个 server 引用时 dedup——编译一次、共享同一无状态 Adapter 实例。
//   - 任一文件缺失/解析失败/校验失败 → 返回 error（中文，带 server 串 + 文件名 + 原因）。
//   - codecs 为空（含 nil）→ 返回 error（不返回空 resolver，避免静默）。
//   - errorsFile 非空但加载失败 → 返回 error（fail loud）。
//
// 顺序：为错误信息稳定，按 server 串排序后遍历。
func LoadCodecResolver(codecDir string, codecs map[string]string, errorsFile string) (CodecResolver, error) {
	if len(codecs) == 0 {
		return nil, fmt.Errorf("codec 加载失败：未声明任何连接的 codec 映射")
	}

	// 可选加载共享 errors.json（一次）。
	var errorMap map[uint64]string
	if errorsFile != "" {
		errPath := resolvePath(codecDir, errorsFile)
		em, err := codec.LoadErrorMap(errPath)
		if err != nil {
			return nil, fmt.Errorf("codec 加载失败：错误码文件 %q：%w", errorsFile, err)
		}
		errorMap = em
	}

	// dedup：同一文件名只编译一次。
	fileCache := make(map[string]Adapter)
	byServer := make(map[string]Adapter, len(codecs))

	// 按 server 串排序遍历，保证错误信息稳定。
	servers := make([]string, 0, len(codecs))
	for s := range codecs {
		servers = append(servers, s)
	}
	sort.Strings(servers)

	for _, server := range servers {
		file := codecs[server]
		if file == "" {
			return nil, fmt.Errorf("codec 加载失败：连接 %q 文件名为空", server)
		}

		// dedup 命中：直接复用已编译实例。
		if cached, ok := fileCache[file]; ok {
			byServer[server] = cached
			continue
		}

		path := resolvePath(codecDir, file)
		schema, err := codec.LoadSchema(path)
		if err != nil {
			return nil, fmt.Errorf("codec 加载失败：连接 %q 文件 %q：%w", server, file, err)
		}
		a, err := NewSchemaAdapter(schema, errorMap)
		if err != nil {
			return nil, fmt.Errorf("codec 加载失败：连接 %q 文件 %q：%w", server, file, err)
		}

		fileCache[file] = a
		byServer[server] = a
	}

	return NewCodecResolver(byServer), nil
}

// resolvePath：errorsFile/codecFile 若为绝对路径则直接使用，否则相对 codecDir 拼。
func resolvePath(codecDir, name string) string {
	if filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(codecDir, name)
}

// PickMetaAdapter 从 resolver 取任一 adapter 作为 gnet Dialer/EventServer 的元信息源
// （仅用于 OnTraffic 的 HeaderSize/BodyLength 帧切割，纯 Go，运行在 gnet 事件循环热路径上）。
//
// 入参 codecMap 用于选 key：按 server 串排序取首个，保证可复现。codecMap 必须非空
// （InferCodecMap 已对空映射 fail loud），否则返回 nil——这会让 NewEventServer 在首次
// OnTraffic 时 panic，启动期即暴露问题（不静默兜底）。
//
// 前提：当前协议 HeaderSize/BodyLength 全局一致（生产 3 份 codec.json 同 frame spec，
// T1.6 同源生成）。多 codec 但 HeaderSize 不一致的场景，per-connection HeaderSize 下沉
// 到 Connection 留到 2-C3 connectionPump；2-C1 仍由 EventServer 持单一元信息源。
func PickMetaAdapter(resolver CodecResolver, codecMap map[string]string) Adapter {
	if len(codecMap) == 0 {
		return nil
	}
	keys := make([]string, 0, len(codecMap))
	for k := range codecMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return resolver.Resolve(keys[0])
}
