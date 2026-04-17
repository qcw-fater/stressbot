// 辅助工具：校验 flow.json 的完整性（节点引用、动作映射）。
// 用法：go run ./cmd/validate conf/flow.json
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"stressbot/engine"
)

func main() {
	path := "conf/flow.json"
	if len(os.Args) >= 2 {
		path = os.Args[1]
	}
	data, err := os.ReadFile(path)
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
	if _, ok := flow.Nodes[flow.StartNode]; !ok {
		fmt.Printf("起始节点不存在: %s\n", flow.StartNode)
		issues++
	}
	for id, n := range flow.Nodes {
		for _, nn := range n.Next {
			if _, ok := flow.Nodes[nn.Node]; !ok {
				fmt.Printf("缺失节点引用: %s -> %s\n", id, nn.Node)
				issues++
			}
		}
		if n.TrueNext != "" {
			if _, ok := flow.Nodes[n.TrueNext]; !ok {
				fmt.Printf("缺失 trueNext: %s -> %s\n", id, n.TrueNext)
				issues++
			}
		}
		if n.FalseNext != "" {
			if _, ok := flow.Nodes[n.FalseNext]; !ok {
				fmt.Printf("缺失 falseNext: %s -> %s\n", id, n.FalseNext)
				issues++
			}
		}
		if n.Type == "action" && n.Action != "" {
			if _, ok := flow.Actions[n.Action]; !ok {
				fmt.Printf("缺失 action: %s -> %s\n", id, n.Action)
				issues++
			}
		}
		for _, lc := range n.ListenCallbacks {
			if lc.Callback == "" {
				continue
			}
			if _, ok := flow.Callbacks[lc.Callback]; !ok {
				fmt.Printf("缺失 callback: %s -> %s\n", id, lc.Callback)
				issues++
			}
		}
	}
	if issues == 0 {
		fmt.Println("validate OK")
	} else {
		fmt.Printf("validate FAILED: %d issue(s)\n", issues)
		os.Exit(1)
	}
}
