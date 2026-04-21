// 辅助工具：校验 flow.json 的完整性（节点引用、动作映射、脚本存在性、孤立节点）。
// 用法：go run ./cmd/validate [flow.json路径] [scripts目录]
//
//	默认 flow.json 路径: conf/flow.json
//	默认 scripts 目录: conf/scripts
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"stressbot/engine"
)

func main() {
	flowPath := "conf/flow.json"
	scriptDir := "conf/scripts"
	if len(os.Args) >= 2 {
		flowPath = os.Args[1]
	}
	if len(os.Args) >= 3 {
		scriptDir = os.Args[2]
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
	fmt.Printf("startNode=%s\nnodes=%d\nactions=%d\ncallbacks=%d\n",
		flow.StartNode, len(flow.Nodes), len(flow.Actions), len(flow.Callbacks))

	issues := 0

	// 1. 起始节点
	if _, ok := flow.Nodes[flow.StartNode]; !ok {
		fmt.Printf("[ERROR] 起始节点不存在: %s\n", flow.StartNode)
		issues++
	}

	// 2. 逐节点校验
	for id, n := range flow.Nodes {
		// 未知类型
		validTypes := map[string]bool{"start": true, "sequence": true, "action": true, "loop": true, "boolean": true, "weighted": true, "wait": true}
		if !validTypes[n.Type] {
			fmt.Printf("[ERROR] 未知节点类型: %s type=%s\n", id, n.Type)
			issues++
		}

		// next 引用
		for _, nn := range n.Next {
			if _, ok := flow.Nodes[nn.Node]; !ok {
				fmt.Printf("[ERROR] 缺失节点引用: %s -> %s\n", id, nn.Node)
				issues++
			}
		}

		// boolean 分支
		if n.TrueNext != "" {
			if _, ok := flow.Nodes[n.TrueNext]; !ok {
				fmt.Printf("[ERROR] 缺失 trueNext: %s -> %s\n", id, n.TrueNext)
				issues++
			}
		}
		if n.FalseNext != "" {
			if _, ok := flow.Nodes[n.FalseNext]; !ok {
				fmt.Printf("[ERROR] 缺失 falseNext: %s -> %s\n", id, n.FalseNext)
				issues++
			}
		}

		// action 引用
		if n.Type == "action" && n.Action != "" {
			if _, ok := flow.Actions[n.Action]; !ok {
				fmt.Printf("[ERROR] 缺失 action: %s -> %s\n", id, n.Action)
				issues++
			}
		}

		// listenCallbacks 引用
		for _, lc := range n.ListenCallbacks {
			if lc.Callback == "" {
				continue
			}
			if _, ok := flow.Callbacks[lc.Callback]; !ok {
				fmt.Printf("[ERROR] 缺失 callback: %s -> %s\n", id, lc.Callback)
				issues++
			}
		}

		// weighted 节点权重校验
		if n.Type == "weighted" {
			if len(n.Next) == 0 {
				fmt.Printf("[WARN] weighted 节点无子节点: %s\n", id)
			}
			totalWeight := 0
			for _, nn := range n.Next {
				totalWeight += nn.Weight
			}
			if totalWeight == 0 && len(n.Next) > 0 {
				fmt.Printf("[WARN] weighted 节点所有权重为 0: %s\n", id)
			}
		}

		// loop 节点校验
		if n.Type == "loop" {
			if n.LoopCount == 0 {
				fmt.Printf("[WARN] loop 节点 loopCount=0 将不执行: %s\n", id)
			}
			if len(n.Next) == 0 {
				fmt.Printf("[WARN] loop 节点无子节点: %s\n", id)
			}
		}

		// wait 节点校验
		if n.Type == "wait" && n.WaitSeconds <= 0 {
			fmt.Printf("[WARN] wait 节点等待时间无效: %s waitSeconds=%.1f\n", id, n.WaitSeconds)
		}

		// boolean 节点条件校验
		if n.Type == "boolean" && n.Condition == "" && n.Action == "" {
			fmt.Printf("[WARN] boolean 节点缺少 condition: %s\n", id)
		}
	}

	// 3. action 定义校验（Lua 脚本存在性）
	for name, action := range flow.Actions {
		if action.Pattern == "lua" && action.Script != "" {
			scriptPath := filepath.Join(scriptDir, action.Script)
			if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
				fmt.Printf("[ERROR] Lua 脚本不存在: action=%s script=%s\n", name, scriptPath)
				issues++
			}
		}
	}

	// 4. callback 定义校验（Lua 脚本存在性）
	for name, cb := range flow.Callbacks {
		if cb.Script != "" {
			scriptPath := filepath.Join(scriptDir, cb.Script)
			if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
				fmt.Printf("[ERROR] callback 脚本不存在: callback=%s script=%s\n", name, scriptPath)
				issues++
			}
		}
	}

	// 5. boolean 条件中的 Lua 脚本存在性
	for id, n := range flow.Nodes {
		cond := n.Condition
		if cond == "" && n.Type == "boolean" {
			cond = n.Action
		}
		if strings.HasPrefix(cond, "lua:") {
			scriptName := cond[4:]
			scriptPath := filepath.Join(scriptDir, scriptName)
			if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
				fmt.Printf("[ERROR] 条件脚本不存在: node=%s script=%s\n", id, scriptPath)
				issues++
			}
		}
	}

	// 6. 孤立节点检测
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
		for _, nn := range n.Next {
			visit(nn.Node)
		}
		if n.TrueNext != "" {
			visit(n.TrueNext)
		}
		if n.FalseNext != "" {
			visit(n.FalseNext)
		}
	}
	visit(flow.StartNode)
	for id := range flow.Nodes {
		if !reachable[id] {
			fmt.Printf("[WARN] 孤立节点（不可达）: %s type=%s\n", id, flow.Nodes[id].Type)
		}
	}

	// 7. 未使用的 action/callback
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

	if issues == 0 {
		fmt.Println("validate OK")
	} else {
		fmt.Printf("validate FAILED: %d error(s)\n", issues)
		os.Exit(1)
	}
}
