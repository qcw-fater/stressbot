# 后端流程模板库与全局基础设施配置设计

## 背景

当前前端「本地流程管理」把已保存流程存放在浏览器 IndexedDB 中。该方案只能在本机浏览器内使用，清理站点数据、无痕模式、换浏览器或换设备都会丢失流程库，也没有完整的导出/备份机制。另一方面，Admin 后端已经在历史归档中用 `task_config_archive.flow_json` 保存每次任务实际运行的 flow 快照，说明后端存储 flow JSON 的技术路径已经存在，但它属于任务历史快照，不是可复用的命名流程库。

本次设计将命名流程库迁移到 Admin 后端 MySQL，同时允许启动任务时在「当前画布」与「已保存流程」之间选择。为避免流程库继续依赖 `history.mysql` 这种错误边界，本次同时收敛 Admin 的基础设施配置：MySQL 和 Redis 都作为全局配置与全局实例，由 AdminServer 统一初始化和管理。

## 目标

1. 已保存流程从浏览器 IndexedDB 迁移到后端 MySQL，支持跨浏览器、跨设备和服务器备份。
2. 启动任务时可以选择已保存流程，运行态画布显示实际启动的流程，监控指标按该流程节点匹配。
3. 当前编辑画布继续作为草稿保存，进入运行态前 stash，返回编辑时恢复。
4. MySQL/Redis 从模块私有配置提升为全局基础设施配置。
5. 历史任务保留实际运行配置快照，同时记录可选的流程模板来源 ID，用于溯源。

## 非目标

- 不把 `flow_template` 做成自包含资源包；流程模板只存 flow 和 layout，不内嵌 Lua、proto、adapter。
- 不用 `flow_template_id` 替代 `task_config_archive.flow_json`。历史任务必须保留实际运行快照。
- 不做旧 `admin-config.json` 字段兼容读取或自动迁移。配置文件按新结构一次性同步。
- 不做单机模式流程库。单机模式没有 Web UI，继续直接使用 `conf/` 配置。
- 不新增用户/权限隔离。当前系统没有用户模型，流程库为全局共享。

## 配置模型

### 新结构

Admin 配置改为全局基础设施配置：

```json
{
  "port": 7718,
  "publicUrl": "http://127.0.0.1:7718",
  "staticDir": "cmd/web/dist",
  "mysql": {
    "host": "127.0.0.1",
    "port": 3306,
    "user": "stressbot",
    "password": "",
    "database": "stressbot",
    "maxOpenConns": 10,
    "maxIdleConns": 5,
    "connMaxLifetime": "1h"
  },
  "redis": {
    "addr": "127.0.0.1:6379",
    "username": "",
    "password": "",
    "db": 0,
    "keyPrefix": "stressbot",
    "defaultClaimTTL": "30s",
    "opTimeout": "2s",
    "dialTimeout": "5s",
    "readTimeout": "2s",
    "writeTimeout": "2s",
    "poolSize": 0
  },
  "history": {
    "retentionDays": 90
  },
  "agentRegistry": {
    "unhealthyAfter": "30s",
    "offlineAfter": "60s"
  },
  "log": {
    "level": "info",
    "path": "log/admin.log",
    "maxSizeMB": 100,
    "maxBackups": 10
  }
}
```

删除旧字段：

- `history.enabled`
- `history.mysql`
- `shared.redis`

### 启用语义

- MySQL 是否可用由 `mysql` 连接字段是否完整决定，例如 `host/user/database` 非空。
- Redis 是否可用由 `redis.addr != ""` 决定。
- History 和 Flow Template 都依赖全局 MySQL；MySQL 未配置时相关接口返回明确 disabled 错误。
- 共享状态依赖全局 Redis；Redis 未配置时 `capabilities.sharedState=false`，前端启动预检继续拦截使用 `share` 的脚本。

## 后端基础设施

### 全局 MySQL 实例

AdminServer 启动时只初始化一个进程级 `*sql.DB`：

```go
type AdminServer struct {
    cfg Config

    db *sql.DB
    redis sharedstate.ResolvedRedisConfig
    redisReady bool

    history *HistoryStore
    flows *FlowTemplateStore
}
```

启动流程：

1. `cfg.MySQL.Enabled()` 为 true 时调用 `openDB(cfg.MySQL)`。
2. `s.db = db`。
3. 调用全局 schema 初始化函数，例如 `initMySQLSchema(db)`。
4. `s.history = NewHistoryStore(db, cfg.History)`。
5. `s.flows = NewFlowTemplateStore(db)`。
6. `Shutdown` 统一关闭 `s.db`。

`HistoryStore` 不再打开或关闭 DB，只持有共享 db 引用。`FlowTemplateStore` 同理。这样 MySQL 成为进程级基础设施，而不是 history 的私有连接。

### 全局 Redis 配置

Redis 连接配置提升到 `cfg.Redis`，Admin 启动时解析并 ping 一次。RedisStore 仍按任务 runID 创建，因为 runID 是共享状态命名空间隔离的一部分：

```go
store := sharedstate.NewRedisStore(s.redis, runID)
```

这保持两个语义：

- Redis 配置是全局唯一。
- 每个任务仍有独立 runID namespace，任务结束时按 runID 清理。

如果未来需要进一步优化 Redis 连接池，可以把 `RedisStore` 改成共享 client + namespace wrapper，但本次不做。

### Schema 文件

`admin/history_schema.go` 改名为 `admin/mysql_schema.go`。该文件不再只属于历史模块，而是 Admin 的 MySQL schema 汇总。`ddlFlowTemplate` 和 `task_history.flow_template_id` 也放在这里。

## MySQL Schema

### flow_template

```sql
CREATE TABLE IF NOT EXISTS flow_template (
    id              VARCHAR(32)  NOT NULL PRIMARY KEY,
    name            VARCHAR(255) NOT NULL,
    flow_json       MEDIUMBLOB   NOT NULL,
    layout_json     MEDIUMBLOB   NULL,
    node_count      INT          NOT NULL DEFAULT 0,
    action_count    INT          NOT NULL DEFAULT 0,
    created_at      DATETIME(3)  NOT NULL,
    updated_at      DATETIME(3)  NOT NULL,
    INDEX idx_updated (updated_at DESC)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

流程模板只保存 flow 与 layout。Lua/proto/adapter 继续走现有资源管理和任务下发链路。

### task_history.flow_template_id

```sql
ALTER TABLE task_history ADD COLUMN flow_template_id VARCHAR(32) NULL;
```

该字段是逻辑外键，不加数据库 FOREIGN KEY。它只表示任务来源于某个流程模板，不参与读取历史 flow。历史实际配置仍以 `task_config_archive.flow_json` 为准。

模板删除时，应用层可以把相关历史记录的 `flow_template_id` 置空，或者读取时把不存在的模板显示为「模板已删除」。历史快照不受影响。

### 升级脚本

`deploy/upgrade.sql` 增加幂等升级段，使用 `INFORMATION_SCHEMA` 守卫创建表和新增列。遵循现有项目约定：数据库只用逻辑外键，不新增物理 FOREIGN KEY。

## 后端流程模板库

新增 `admin/flow_template.go`，提供 `FlowTemplateStore`：

```go
type FlowTemplateStore struct {
    db *sql.DB
}

func NewFlowTemplateStore(db *sql.DB) *FlowTemplateStore
func (s *FlowTemplateStore) Create(ctx context.Context, req FlowTemplateSaveRequest) (*FlowTemplateDetail, error)
func (s *FlowTemplateStore) List(ctx context.Context) ([]FlowTemplateSummary, error)
func (s *FlowTemplateStore) Get(ctx context.Context, id string) (*FlowTemplateDetail, error)
func (s *FlowTemplateStore) Update(ctx context.Context, id string, req FlowTemplateSaveRequest) (*FlowTemplateDetail, error)
func (s *FlowTemplateStore) Delete(ctx context.Context, id string) error
```

`List` 只返回摘要，不返回 `flow_json/layout_json`：

```go
type FlowTemplateSummary struct {
    ID string `json:"id"`
    Name string `json:"name"`
    NodeCount int `json:"nodeCount"`
    ActionCount int `json:"actionCount"`
    CreatedAt time.Time `json:"createdAt"`
    UpdatedAt time.Time `json:"updatedAt"`
}
```

`Get/Create/Update` 返回完整 detail：

```go
type FlowTemplateDetail struct {
    FlowTemplateSummary
    Flow json.RawMessage `json:"flow"`
    Layout json.RawMessage `json:"layout,omitempty"`
}
```

服务端在保存时计算 `node_count/action_count`，并校验 name 非空、长度不超过前端现有的 80 字符限制。

## 后端 API

新增端点：

- `GET /sbot/flows`：流程模板列表。
- `POST /sbot/flows`：创建流程模板。
- `GET /sbot/flows/{id}`：读取完整流程模板。
- `PUT /sbot/flows/{id}`：覆盖流程模板或重命名。
- `DELETE /sbot/flows/{id}`：删除流程模板。

AdminServer 中 `flows == nil` 时返回 `FLOW_LIBRARY_DISABLED`。错误码沿用现有 API 错误体系，新增：

- `FLOW_LIBRARY_DISABLED`
- `FLOW_TEMPLATE_NOT_FOUND`

## 历史归档溯源

`Task` 或 `TaskConfig` 增加可选 `FlowTemplateID`。选库启动时，前端通过 multipart 附带 `flowTemplateId`。

`handleCreateTask` 解析该字段并保存到任务结构。任务终态归档时：

- `task_config_archive.flow_json` 继续保存完整 flow 快照。
- `task_history.flow_template_id` 保存来源模板 ID。

历史列表与详情可以展示来源模板名称。若模板被删除，展示空或「模板已删除」。不通过 `flow_template` 读取历史配置。

## 前端流程库

新增 `cmd/web/src/services/flowsApi.ts`，封装 `/flows` 端点。`flowManagerStore.ts` 保持现有导出函数签名，但底层从 IndexedDB 改为 `flowsApi`：

- `saveFlow(name, flow, layout, existingId?)`
- `getFlow(id)`
- `listFlows()`
- `renameFlow(id, name)`
- `deleteFlow(id)`

这样 `FlowManagerModal` 的主要逻辑可以保持不变。它不再是「本地流程管理」，UI 文案应改为「流程管理」或「流程模板」。

### 本地 IndexedDB 迁移

为了保护已有用户的 IndexedDB 流程，保留一个只用于迁移的 legacy reader。首次打开流程管理时：

1. 后端 `listFlows()` 返回空。
2. legacy IndexedDB 中存在流程。
3. 弹提示：「检测到 N 个本地流程，是否上传到服务器？」
4. 用户确认后批量 `POST /sbot/flows`。
5. 迁移成功后删除 legacy IndexedDB 或标记已迁移。

这是一次性迁移，不是长期降级方案。后续流程库只以后端为准。

## 启动弹窗选择流程

`TaskStartModal` 新增「流程来源」区域，位于任务名上方：

- `Segmented`: 当前画布 / 已保存流程。
- 默认当前画布，不持久化上次选择。
- 选择已保存流程时显示 `Select` 下拉，数据来自 `listFlows()`。
- 下拉项显示名称、更新时间、节点数。

抽象出：

```ts
async function getSelectedFlow(): Promise<{ flow: FlowJson; layout?: FlowLayout; flowTemplateId?: string }>
```

当前画布：从 `flowStore.toTaskFlow()` 和 `flowStore.layout` 获取。

已保存流程：通过 `getFlow(id)` 读取后端 detail。

`TaskStartModal` 中所有 flow 相关操作都使用 `getSelectedFlow()`：

- 打开弹窗时的脚本引用统计与 gap-fill。
- 点击启动时的 `syncFlowScriptsToIdb(flow)`。
- `checkTaskResourcesAgainstBaseline(flow)`。
- `startTask` 入参。

## startTask 数据流

`StartTaskOptions` 改为显式携带 flow：

```ts
export interface StartTaskOptions {
  name: string;
  totalBots: number;
  robotConfig: RobotConfig;
  deadline?: string | null;
  flow: FlowJson;
  flowLayout?: FlowLayout;
  flowTemplateId?: string;
}
```

`startTask` 不再读取当前画布，而是使用 `opts.flow`。这消除隐式数据源，确保当前画布启动和模板启动走同一条链路。

启动成功后调用共享函数：

```ts
stashAndReplaceCanvas(opts.flow, opts.flowLayout)
```

该函数负责：

1. 把当前编辑稿 flow + layout stash 到 LocalStorage。
2. `loadFromTaskFlow(flow, layout)` 替换为实际运行流程。
3. 清空旧监控数据和 metrics provider。

`attachToActive` 拉取远端 flow 后也复用该函数，保证所有进入运行态的路径都统一处理草稿与运行画布。

## 资源关系

`flow_template` 不存 Lua/proto/adapter。理由：

- flow.json 只引用脚本名和 proto message 名。
- 资源已有独立管理体系：前端 IDB + 服务器 conf 基线 + 资源冲突同步。
- 启动任务时已根据 flow 收集引用脚本，并全量提交 proto 与 adapter。
- 模板内嵌资源会造成重复存储和资源更新不同步。

模板完整性在启动时校验。缺脚本时沿用现有 `missingScripts` 提示。

## UI 文案

遵守前端约定，避免暴露 Agent/Admin/IDB 等技术术语：

- 「本地流程管理」改为「流程管理」或「流程模板」。
- 「Agent」仍按现有 UI 习惯显示为「节点」。
- 迁移提示不提 IndexedDB，使用「检测到浏览器本地保存的流程」。

## 验证计划

1. `go build ./...`
2. `cd cmd/web && npx tsc -b`
3. `cd cmd/web && npm run test`
4. 使用新 `conf/admin-config.json` 启动 Admin，确认 MySQL/Redis 能力展示正确。
5. MySQL 未配置时，历史与流程库接口返回 disabled，其他非 DB 功能可启动。
6. Redis 未配置时，`/sbot/capabilities` 返回 `sharedState=false`，使用 `share` 的脚本启动前被拦截。
7. 创建/重命名/覆盖/删除流程模板。
8. 从流程模板启动任务，确认 multipart 中 `flow.json` 为模板 flow，运行态画布为模板 flow，返回编辑恢复原草稿。
9. 任务终态后历史归档保留 `task_config_archive.flow_json` 快照，并记录 `flow_template_id`。
