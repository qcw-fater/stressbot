// Package runner 装配单机模式与 Agent 任务共用的数据面运行资源。
package runner

// ResourcePaths 描述一次压测运行所需的资源位置。
type ResourcePaths struct {
	Flow    string
	Proto   string
	Scripts string
	Adapter string
}
