// 辅助工具：校验 flow.json 的完整性（节点引用、动作映射、脚本存在性、孤立节点）。
// 支持深度校验模式：
//
//	go run ./cmd/validate [--check-lua] [--check-proto] [--proto-dir dir] [flow.json路径] [scripts目录]
//
// 默认 flow.json 路径: conf/flow.json
// 默认 scripts 目录: conf/scripts
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"stressbot/engine"
	"stressbot/protox"
	stresslog "stressbot/utils/log"

	lua "github.com/yuin/gopher-lua"
)

func main() {
	checkLua := flag.Bool("check-lua", false, "检查 Lua 脚本语法和入口函数")
	checkProto := flag.Bool("check-proto", false, "检查 proto 消息名是否存在")
	protoDir := flag.String("proto-dir", "conf/proto", "proto 文件目录")
	flag.Parse()

	// 位置参数：flag.Parse 后 flag.Args() 返回非 flag 参数
	args := flag.Args()
	flowPath := "conf/flow.json"
	scriptDir := "conf/scripts"
	if len(args) >= 1 {
		flowPath = args[0]
	}
	if len(args) >= 2 {
		scriptDir = args[1]
	}

	data, err := os.ReadFile(flowPath)
	if err != nil {
		fmt.Printf("读取失败: %v\n", err)
		os.Exit(1)
	}
	flow := &engine.TaskFlow{}
	if err := json.Unmarshal(data, flow); err != nil {
		fmt.Printf("解析失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("defaultDelayMs=%d\nnodes=%d\nactions=%d\ncallbacks=%d\n",
		flow.DefaultDelayMs, len(flow.Nodes), len(flow.Actions), len(flow.Callbacks))

	issues := 0

	// ── 节点校验（原有逻辑）────────────────────────────────────────
	validateNodes(flow, scriptDir, &issues)

	// ── Phase A：结构校验（始终执行）─────────────────────────────────
	validateActionPatterns(flow, &issues)
	validatePatternFields(flow, &issues)
	validateBindings(flow, &issues)

	// ── Phase B：Lua 语法 + 入口函数 ─────────────────────────────────
	if *checkLua {
		validateLua(flow, scriptDir, &issues)
	}

	// ── Phase C：Proto 消息名 ────────────────────────────────────────
	var registry *protox.Registry
	if *checkProto {
		// protox 使用 zap 日志，需初始化一个最小 logger 避免空指针
		stresslog.InitLog(os.DevNull, "validate", &stresslog.Config{PrintConsole: false, LogLevel: "warn"}, "warn")
		loader := protox.NewLoader([]string{*protoDir}, nil)
		files, err := loader.Load()
		if err != nil {
			fmt.Printf("[WARN] proto 加载失败，跳过 proto 校验: %v\n", err)
		} else {
			registry = protox.NewRegistry(files)
			validateProtoNames(flow, registry, &issues)
		}
	}

	// ── 孤立节点检测 ─────────────────────────────────────────────────
	detectOrphanNodes(flow)

	// ── 未使用的 action/callback ────────────────────────────────────
	detectUnusedDefs(flow)

	if issues == 0 {
		fmt.Println("validate OK")
	} else {
		fmt.Printf("validate FAILED: %d error(s)\n", issues)
		os.Exit(1)
	}
}

// ──────────────────────────────────────────────────────────────────
// 节点校验（原有逻辑，提取为函数）
// ──────────────────────────────────────────────────────────────────

func validateNodes(flow *engine.TaskFlow, scriptDir string, issues *int) {
	// main 入口节点
	if _, ok := flow.Nodes["main"]; !ok {
		fmt.Printf("[ERROR] 入口节点不存在: main\n")
		*issues++
	}

	validTypes := map[string]bool{
		"sequence": true, "action": true, "loop": true, "boolean": true,
		"weighted": true, "wait": true, "break": true, "continue": true,
	}

	for id, n := range flow.Nodes {
		if !validTypes[n.Type] {
			fmt.Printf("[ERROR] 未知节点类型: %s type=%s\n", id, n.Type)
			*issues++
		}

		if n.Type == "sequence" {
			for _, childID := range n.Next {
				if _, ok := flow.Nodes[childID]; !ok {
					fmt.Printf("[ERROR] 缺失节点引用: %s -> %s\n", id, childID)
					*issues++
				}
			}
			if len(n.Next) == 0 {
				fmt.Printf("[WARN] sequence 节点无子节点: %s\n", id)
			}
		}

		if n.Type == "loop" {
			if n.Body == "" {
				fmt.Printf("[ERROR] loop 节点缺少 body: %s\n", id)
				*issues++
			} else if _, ok := flow.Nodes[n.Body]; !ok {
				fmt.Printf("[ERROR] 缺失 body 引用: %s -> %s\n", id, n.Body)
				*issues++
			}
			if n.LoopCount == 0 {
				fmt.Printf("[WARN] loop 节点 loopCount=0 将不执行: %s\n", id)
			}
			checkLuaCondition(id, "condition", n.Condition, scriptDir, issues)
			checkLuaCondition(id, "breakCondition", n.BreakCondition, scriptDir, issues)
		}

		if n.Type == "boolean" {
			if n.Condition == "" {
				fmt.Printf("[WARN] boolean 节点缺少 condition: %s\n", id)
			}
			checkLuaCondition(id, "condition", n.Condition, scriptDir, issues)
			if n.TrueNext != "" {
				if _, ok := flow.Nodes[n.TrueNext]; !ok {
					fmt.Printf("[ERROR] 缺失 trueNext: %s -> %s\n", id, n.TrueNext)
					*issues++
				}
			}
			if n.FalseNext != "" {
				if _, ok := flow.Nodes[n.FalseNext]; !ok {
					fmt.Printf("[ERROR] 缺失 falseNext: %s -> %s\n", id, n.FalseNext)
					*issues++
				}
			}
		}

		if n.Type == "action" && n.Action != "" {
			if _, ok := flow.Actions[n.Action]; !ok {
				fmt.Printf("[ERROR] 缺失 action: %s -> %s\n", id, n.Action)
				*issues++
			}
		}

		// ListenRef 校验（server 格式 + callback 引用）
		for _, lc := range n.ListenCallbacks {
			if lc.Server != "" {
				if !strings.HasPrefix(lc.Server, "tcp:") && !strings.HasPrefix(lc.Server, "udp:") {
					fmt.Printf("[ERROR] ListenRef server 格式错误: node=%s server=%s (应为 tcp:<name> 或 udp:<name>)\n", id, lc.Server)
					*issues++
				} else {
					parts := strings.SplitN(lc.Server, ":", 2)
					if len(parts) == 2 && parts[1] == "" {
						fmt.Printf("[ERROR] ListenRef server 缺少服务名: node=%s server=%s\n", id, lc.Server)
						*issues++
					}
				}
			}
			if lc.Callback == "" {
				continue
			}
			if _, ok := flow.Callbacks[lc.Callback]; !ok {
				fmt.Printf("[ERROR] 缺失 callback: %s -> %s\n", id, lc.Callback)
				*issues++
			}
		}

		if n.Type == "weighted" {
			if len(n.Options) == 0 {
				fmt.Printf("[WARN] weighted 节点无选项: %s\n", id)
			}
			totalWeight := 0
			for _, opt := range n.Options {
				totalWeight += opt.Weight
				if _, ok := flow.Nodes[opt.Node]; !ok {
					fmt.Printf("[ERROR] 缺失 weighted 选项节点: %s -> %s\n", id, opt.Node)
					*issues++
				}
			}
			if totalWeight == 0 && len(n.Options) > 0 {
				fmt.Printf("[WARN] weighted 节点所有权重为 0: %s\n", id)
			}
		}

		if n.Type == "wait" && n.WaitMs <= 0 {
			fmt.Printf("[WARN] wait 节点等待时间无效: %s waitMs=%d\n", id, n.WaitMs)
		}
	}

	// action Lua 脚本文件存在性
	for name, action := range flow.Actions {
		if action.Pattern == engine.PatternLua && action.Script != "" {
			scriptPath := filepath.Join(scriptDir, action.Script)
			if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
				fmt.Printf("[ERROR] Lua 脚本不存在: action=%s script=%s\n", name, scriptPath)
				*issues++
			}
		}
	}

	// callback Lua 脚本文件存在性
	for name, cb := range flow.Callbacks {
		if cb.Script != "" {
			scriptPath := filepath.Join(scriptDir, cb.Script)
			if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
				fmt.Printf("[ERROR] callback 脚本不存在: callback=%s script=%s\n", name, scriptPath)
				*issues++
			}
		}
	}
}

// ──────────────────────────────────────────────────────────────────
// Phase A：结构校验
// ──────────────────────────────────────────────────────────────────

// validateActionPatterns 校验 action pattern 值合法性。
func validateActionPatterns(flow *engine.TaskFlow, issues *int) {
	validPatterns := map[string]bool{
		engine.PatternTCPSend: true, engine.PatternTCPRequest: true,
		engine.PatternLua: true, engine.PatternConnect: true,
		engine.PatternConnectUDP: true, engine.PatternExchangeKey: true,
		engine.PatternClose: true, engine.PatternClearState: true,
		engine.PatternUDPSendProto: true, engine.PatternWaitListen: true,
		engine.PatternSetState: true,
	}
	for name, action := range flow.Actions {
		if action.Pattern == "" {
			fmt.Printf("[WARN] action 缺少 pattern: %s\n", name)
			continue
		}
		if !validPatterns[action.Pattern] {
			fmt.Printf("[ERROR] action 未知 pattern: %s pattern=%s\n", name, action.Pattern)
			*issues++
		}
	}
}

// validatePatternFields 校验每种 pattern 的必填字段。
func validatePatternFields(flow *engine.TaskFlow, issues *int) {
	for name, action := range flow.Actions {
		switch action.Pattern {
		case engine.PatternTCPSend, engine.PatternTCPRequest, engine.PatternUDPSendProto:
			if action.Service == "" {
				fmt.Printf("[ERROR] action 缺少 service: %s pattern=%s\n", name, action.Pattern)
				*issues++
			}
			if action.Route == nil {
				fmt.Printf("[ERROR] action 缺少 route: %s pattern=%s\n", name, action.Pattern)
				*issues++
			}
		case engine.PatternLua:
			if action.Script == "" {
				fmt.Printf("[ERROR] action 缺少 script: %s\n", name)
				*issues++
			}
		case engine.PatternConnect, engine.PatternConnectUDP:
			if action.Service == "" {
				fmt.Printf("[ERROR] action 缺少 service: %s pattern=%s\n", name, action.Pattern)
				*issues++
			}
			if action.Address == "" {
				fmt.Printf("[ERROR] action 缺少 address: %s pattern=%s\n", name, action.Pattern)
				*issues++
			}
		case engine.PatternExchangeKey:
			if action.Service == "" {
				fmt.Printf("[ERROR] action 缺少 service: %s\n", name)
				*issues++
			}
			if action.Route == nil {
				fmt.Printf("[ERROR] action 缺少 route: %s\n", name)
				*issues++
			}
		case engine.PatternClose:
			if action.Service == "" {
				fmt.Printf("[ERROR] action 缺少 service: %s\n", name)
				*issues++
			}
			if action.Target != "" && action.Target != "tcp" && action.Target != "udp" {
				fmt.Printf("[ERROR] action close target 无效: %s target=%s (应为 tcp 或 udp)\n", name, action.Target)
				*issues++
			}
		case engine.PatternClearState:
			if len(action.Keys) == 0 {
				fmt.Printf("[ERROR] action clearState 缺少 keys: %s\n", name)
				*issues++
			}
		case engine.PatternWaitListen:
			if action.Service == "" {
				fmt.Printf("[ERROR] action 缺少 service: %s\n", name)
				*issues++
			}
			if action.Route == nil {
				fmt.Printf("[ERROR] action 缺少 route: %s\n", name)
				*issues++
			}
			if action.S2CProto == "" {
				fmt.Printf("[ERROR] action waitListen 缺少 s2cProto: %s\n", name)
				*issues++
			}
		}
	}
}

// validateBindings 校验所有 action 的 binding 配置。
func validateBindings(flow *engine.TaskFlow, issues *int) {
	for name, action := range flow.Actions {
		validateBindingTree(fmt.Sprintf("action=%s", name), action.Bindings, issues)
	}
}

// validateBindingTree 递归校验 binding 列表。
func validateBindingTree(prefix string, bindings []engine.FieldBind, issues *int) {
	validTypes := map[string]bool{
		"": true, "fixed": true,
		"state": true, "stateFirst": true, "stateRandom": true, "stateRandomN": true,
		"stateMapKey": true, "stateMapValue": true,
		"randomPick": true, "randomPickN": true, "randomPickMap": true,
		"randomExclude": true,
		"randomInt": true, "randomBool": true, "randomString": true,
		"nested": true, "nestedList": true, "listSize": true,
	}

	for i, fb := range bindings {
		label := fmt.Sprintf("%s.bindings[%d]", prefix, i)
		if fb.Field == "" && fb.StoreAs == "" && fb.Type != "nested" && fb.Type != "nestedList" {
			fmt.Printf("[WARN] binding 缺少 field 和 storeAs: %s\n", label)
		}

		if !validTypes[fb.Type] {
			fmt.Printf("[ERROR] 未知 binding type: %s type=%s\n", label, fb.Type)
			*issues++
			continue
		}

		switch fb.Type {
		case "state", "stateFirst", "stateRandom",
			"stateMapKey", "stateMapValue", "listSize":
			if fb.Source == "" {
				fmt.Printf("[ERROR] binding 缺少 source: %s type=%s\n", label, fb.Type)
				*issues++
			}
		case "stateRandomN":
			if fb.Source == "" {
				fmt.Printf("[ERROR] binding 缺少 source: %s type=%s\n", label, fb.Type)
				*issues++
			}
			if fb.Count <= 0 {
				fmt.Printf("[ERROR] binding stateRandomN count <= 0: %s count=%d\n", label, fb.Count)
				*issues++
			}
		case "randomPick", "randomPickN":
			if len(fb.Values) == 0 {
				fmt.Printf("[ERROR] binding 缺少 values: %s type=%s\n", label, fb.Type)
				*issues++
			}
			if fb.Type == "randomPickN" && fb.Count <= 0 {
				fmt.Printf("[ERROR] binding count <= 0: %s type=%s count=%d\n", label, fb.Type, fb.Count)
				*issues++
			}
		case "randomPickMap":
			if len(fb.Values) == 0 {
				fmt.Printf("[ERROR] binding 缺少 values: %s type=%s\n", label, fb.Type)
				*issues++
			}
			if fb.KeySource == "" {
				fmt.Printf("[ERROR] binding 缺少 keySource: %s type=%s\n", label, fb.Type)
				*issues++
			}
		case "randomExclude":
			if len(fb.Values) == 0 && fb.Source == "" {
				fmt.Printf("[ERROR] binding randomExclude 缺少 values 和 source: %s\n", label)
				*issues++
			}
		case "randomInt":
			if fb.Min >= fb.Max {
				fmt.Printf("[WARN] binding randomInt min >= max: %s min=%d max=%d\n", label, fb.Min, fb.Max)
			}
		case "randomString":
			if fb.Length <= 0 {
				fmt.Printf("[ERROR] binding randomString length <= 0: %s length=%d\n", label, fb.Length)
				*issues++
			}
		case "nested":
			if fb.Message == "" {
				fmt.Printf("[ERROR] binding nested 缺少 message: %s\n", label)
				*issues++
			}
			if len(fb.Bindings) == 0 {
				fmt.Printf("[WARN] binding nested 无 bindings: %s\n", label)
			}
			validateBindingTree(label, fb.Bindings, issues)
		case "nestedList":
			if len(fb.Items) == 0 {
				fmt.Printf("[ERROR] binding nestedList 缺少 items: %s\n", label)
				*issues++
			}
			for j, item := range fb.Items {
				itemLabel := fmt.Sprintf("%s.items[%d]", label, j)
				if item.Message == "" {
					fmt.Printf("[ERROR] binding nestedList item 缺少 message: %s\n", itemLabel)
					*issues++
				}
				validateBindingTree(itemLabel, item.Bindings, issues)
			}
		}

		// FilterDef op 校验
		validateFilterOps(label, fb.Filters, issues)
	}
}

// validateFilterOps 校验过滤器 op 合法性。
func validateFilterOps(prefix string, filters []engine.FilterDef, issues *int) {
	validOps := map[string]bool{
		"": true, "==": true, "!=": true, ">": true, ">=": true, "<": true, "<=": true,
		"eq": true, "neq": true, "gt": true, "gte": true, "lt": true, "lte": true,
		"contains": true, "in": true,
		"timeWindow": true, "dailyTimeWindow": true,
		"notNil": true, "isNil": true,
	}
	for i, f := range filters {
		if !validOps[f.Op] {
			fmt.Printf("[ERROR] 过滤器未知 op: %s.filters[%d] op=%s\n", prefix, i, f.Op)
			*issues++
		}
	}
}

// ──────────────────────────────────────────────────────────────────
// Phase B：Lua 语法 + 入口函数校验
// ──────────────────────────────────────────────────────────────────

func validateLua(flow *engine.TaskFlow, scriptDir string, issues *int) {
	type scriptCheck struct {
		path    string
		entryFn string
		context string
	}

	var scripts []scriptCheck

	// action 脚本（pattern=lua）→ 需要 execute()
	for name, action := range flow.Actions {
		if action.Pattern == engine.PatternLua && action.Script != "" {
			scripts = append(scripts, scriptCheck{
				path: action.Script, entryFn: "execute",
				context: fmt.Sprintf("action=%s", name),
			})
		}
	}

	// callback 脚本 → 需要 onMessage()
	for name, cb := range flow.Callbacks {
		if cb.Script != "" {
			scripts = append(scripts, scriptCheck{
				path: cb.Script, entryFn: "onMessage",
				context: fmt.Sprintf("callback=%s", name),
			})
		}
	}

	// 条件脚本（lua: 前缀）→ 需要 execute()
	for id, n := range flow.Nodes {
		addCond := func(field, val string) {
			if strings.HasPrefix(val, "lua:") {
				scripts = append(scripts, scriptCheck{
					path: val[4:], entryFn: "execute",
					context: fmt.Sprintf("node=%s %s", id, field),
				})
			}
		}
		if n.Type == "loop" {
			addCond("condition", n.Condition)
			addCond("breakCondition", n.BreakCondition)
		}
		if n.Type == "boolean" {
			addCond("condition", n.Condition)
		}
	}

	// 去重：同一脚本可能有多个入口函数要求
	type req struct{ entryFn, context string }
	scriptReqs := make(map[string][]req)
	for _, sc := range scripts {
		scriptReqs[sc.path] = append(scriptReqs[sc.path], req{sc.entryFn, sc.context})
	}

	for relPath, entries := range scriptReqs {
		fullPath := filepath.Join(scriptDir, relPath)
		L := lua.NewState()

		fn, err := L.LoadFile(fullPath)
		if err != nil {
			fmt.Printf("[ERROR] Lua 语法错误: %s script=%s err=%v\n", entries[0].context, fullPath, err)
			*issues++
			L.Close()
			continue
		}

		// 执行脚本块以定义全局函数
		// 注意：脚本 require 了框架模块（network/robot/log 等），裸 LState 无法解析，
		// 所以执行失败是预期的，降级为 WARN。语法错误已在 LoadFile 阶段捕获。
		L.Push(fn)
		if err := L.PCall(0, 0, nil); err != nil {
			fmt.Printf("[WARN] Lua require 模块失败（正常，缺少框架环境）: %s script=%s\n", entries[0].context, fullPath)
			L.Close()
			continue
		}

		// 检查入口函数存在
		for _, e := range entries {
			if L.GetGlobal(e.entryFn) == lua.LNil {
				fmt.Printf("[ERROR] Lua 脚本缺少 %s 函数: %s script=%s\n", e.entryFn, e.context, fullPath)
				*issues++
			}
		}

		L.Close()
	}
}

// ──────────────────────────────────────────────────────────────────
// Phase C：Proto 消息名校验
// ──────────────────────────────────────────────────────────────────

func validateProtoNames(flow *engine.TaskFlow, registry *protox.Registry, issues *int) {
	for name, action := range flow.Actions {
		if action.C2SProto != "" {
			if _, ok := registry.Lookup(action.C2SProto); !ok {
				fmt.Printf("[ERROR] proto 未找到: action=%s c2sProto=%s\n", name, action.C2SProto)
				*issues++
			}
		}
		if action.S2CProto != "" {
			if _, ok := registry.Lookup(action.S2CProto); !ok {
				fmt.Printf("[ERROR] proto 未找到: action=%s s2cProto=%s\n", name, action.S2CProto)
				*issues++
			}
		}
		checkBindingMessages(fmt.Sprintf("action=%s", name), action.Bindings, registry, issues)
	}

	for name, cb := range flow.Callbacks {
		if cb.S2CProto != "" {
			if _, ok := registry.Lookup(cb.S2CProto); !ok {
				fmt.Printf("[ERROR] proto 未找到: callback=%s s2cProto=%s\n", name, cb.S2CProto)
				*issues++
			}
		}
	}
}

// checkBindingMessages 递归校验 nested binding 的 message 字段。
func checkBindingMessages(prefix string, bindings []engine.FieldBind, registry *protox.Registry, issues *int) {
	for i, fb := range bindings {
		if fb.Type == "nested" && fb.Message != "" {
			if _, ok := registry.Lookup(fb.Message); !ok {
				fmt.Printf("[ERROR] proto 未找到: %s.bindings[%d] message=%s\n", prefix, i, fb.Message)
				*issues++
			}
			checkBindingMessages(fmt.Sprintf("%s.bindings[%d]", prefix, i), fb.Bindings, registry, issues)
		}
		if fb.Type == "nestedList" {
			for j, item := range fb.Items {
				if item.Message != "" {
					if _, ok := registry.Lookup(item.Message); !ok {
						fmt.Printf("[ERROR] proto 未找到: %s.bindings[%d].items[%d] message=%s\n", prefix, i, j, item.Message)
						*issues++
					}
					checkBindingMessages(fmt.Sprintf("%s.bindings[%d].items[%d]", prefix, i, j), item.Bindings, registry, issues)
				}
			}
		}
	}
}

// ──────────────────────────────────────────────────────────────────
// 原有辅助函数
// ──────────────────────────────────────────────────────────────────

func checkLuaCondition(nodeID, field, value, scriptDir string, issues *int) {
	if strings.HasPrefix(value, "lua:") {
		scriptName := value[4:]
		scriptPath := filepath.Join(scriptDir, scriptName)
		if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
			fmt.Printf("[ERROR] 条件脚本不存在: node=%s %s=%s\n", nodeID, field, scriptPath)
			*issues++
		}
	}
}

func detectOrphanNodes(flow *engine.TaskFlow) {
	reachable := make(map[string]bool)
	var visit func(string)
	visit = func(nodeID string) {
		if reachable[nodeID] {
			return
		}
		reachable[nodeID] = true
		n, ok := flow.Nodes[nodeID]
		if !ok {
			return
		}
		for _, childID := range n.Next {
			visit(childID)
		}
		if n.Body != "" {
			visit(n.Body)
		}
		if n.TrueNext != "" {
			visit(n.TrueNext)
		}
		if n.FalseNext != "" {
			visit(n.FalseNext)
		}
		for _, opt := range n.Options {
			visit(opt.Node)
		}
	}
	visit("main")
	for id := range flow.Nodes {
		if !reachable[id] {
			fmt.Printf("[WARN] 孤立节点（不可达）: %s type=%s\n", id, flow.Nodes[id].Type)
		}
	}
}

func detectUnusedDefs(flow *engine.TaskFlow) {
	usedActions := make(map[string]bool)
	usedCallbacks := make(map[string]bool)
	for _, n := range flow.Nodes {
		if n.Action != "" {
			usedActions[n.Action] = true
		}
		for _, lc := range n.ListenCallbacks {
			if lc.Callback != "" {
				usedCallbacks[lc.Callback] = true
			}
		}
	}
	for name := range flow.Actions {
		if !usedActions[name] {
			fmt.Printf("[WARN] 未使用的 action: %s\n", name)
		}
	}
	for name := range flow.Callbacks {
		if !usedCallbacks[name] {
			fmt.Printf("[WARN] 未使用的 callback: %s\n", name)
		}
	}
}
