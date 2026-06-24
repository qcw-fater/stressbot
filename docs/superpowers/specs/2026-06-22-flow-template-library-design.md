# 后端流程模板库与选择流程启动设计

## 背景

当前前端「本地流程管理」把保存的流程放在浏览器本地存储中。该方案只适合临时草稿：清理站点数据、换浏览器或换设备都会丢失流程库，也不便于团队共享和服务器备份。Admin 后端已经在历史归档中保存每次任务实际运行的 `flow.json` 快照，但历史快照不是可复用、可重命名、可覆盖的命名流程库。

本设计将「已保存流程」切换为 Admin 后端 MySQL 中的流程模板库，并在启动任务时允许用户选择「当前画布」或「已保存流程」。用户已明确选择不迁移旧浏览器本地流程：上线后流程管理只以后端为准，旧 IndexedDB 流程不再显示，也不做自动上传。

## 目标

1. 新增后端流程模板库，保存命名流程的 `flow` 与 `layout`。
2. 前端流程管理改为调用后端模板 API，不再把流程库保存在 IndexedDB。
3. 启动任务时可以选择当前画布或已保存流程。
4. 主动启动和挂载运行中任务都使用同一套「stash 草稿 + 替换运行态画布」逻辑。
5. 历史任务继续保存实际运行快照，并可记录来源模板 ID 作为溯源信息。

## 非目标

- 不做旧浏览器本地流程迁移。
- 不保留本地流程库与服务器流程库双入口。
- 不把 Lua、proto、adapter、errors.json 内嵌到流程模板中。
- 不用流程模板替代历史归档里的 `task_config_archive.flow_json`。
- 不新增用户、权限、项目空间或多租户隔离。
- 不做单机模式流程库；单机模式继续通过 `-flow` 或 `conf/flow/flow.json` 启动。

## 数据模型

### flow_template 表

在 `admin/mysql_schema.go` 中加入 Admin 全局 MySQL schema：

```sql
CREATE TABLE IF NOT EXISTS flow_template (
    id           VARCHAR(32)  NOT NULL PRIMARY KEY,
    name         VARCHAR(80)  NOT NULL,
    flow_json    MEDIUMBLOB   NOT NULL,
    layout_json  MEDIUMBLOB   NULL,
    node_count   INT          NOT NULL DEFAULT 0,
    action_count INT          NOT NULL DEFAULT 0,
    created_at   DATETIME(3)  NOT NULL,
    updated_at   DATETIME(3)  NOT NULL,
    INDEX idx_flow_template_updated (updated_at DESC)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

`name` 长度沿用当前前端 `FLOW_NAME_MAX_LENGTH = 80`。`node_count` 和 `action_count` 由服务端保存时从 `flow_json` 计算，用于列表展示，不由前端信任传入值。

### task_history.flow_template_id

在 `task_history` 增加可空列：

```sql
ALTER TABLE task_history ADD COLUMN flow_template_id VARCHAR(32) NULL;
```

该字段是逻辑外键，不添加数据库 `FOREIGN KEY`。模板删除或覆盖不会影响历史任务读取。历史实际运行配置仍以 `task_config_archive.flow_json` 为准。

## 后端组件

新增 `admin/flow_template.go`，提供 `FlowTemplateStore`。它只依赖 AdminServer 共享的 `*sql.DB`，不自行打开或关闭数据库连接。

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

`AdminServer` 增加可选字段：

```go
flows *FlowTemplateStore
```

当全局 MySQL 未配置或打开失败时，`flows == nil`，流程模板 API 返回明确的禁用错误，不影响非 MySQL 功能启动。

## 后端 API

新增端点：

```text
GET    /sbot/flows
POST   /sbot/flows
GET    /sbot/flows/{id}
PUT    /sbot/flows/{id}
DELETE /sbot/flows/{id}
```

### 请求与响应

保存请求：

```json
{
  "name": "排位流程",
  "flow": {},
  "layout": {}
}
```

列表响应只返回摘要：

```json
[
  {
    "id": "...",
    "name": "排位流程",
    "nodeCount": 12,
    "actionCount": 8,
    "createdAt": "2026-06-22T10:00:00+08:00",
    "updatedAt": "2026-06-22T10:00:00+08:00"
  }
]
```

详情响应返回完整内容：

```json
{
  "id": "...",
  "name": "排位流程",
  "nodeCount": 12,
  "actionCount": 8,
  "createdAt": "2026-06-22T10:00:00+08:00",
  "updatedAt": "2026-06-22T10:00:00+08:00",
  "flow": {},
  "layout": {}
}
```

### 错误码

在 `admin/errors.go` 增加：

- `FLOW_LIBRARY_DISABLED`：流程库不可用，通常是服务器未配置 MySQL。
- `FLOW_TEMPLATE_NOT_FOUND`：流程模板不存在。

保存和更新时校验：

- `name` 去首尾空白后不能为空。
- `name` 长度不能超过 80。
- `flow` 必须是合法 JSON 对象，并至少能解析出 `nodes` 与 `actions` 用于统计。
- `layout` 可为空；为空时不写或写 `NULL`。

## 任务创建与历史溯源

`Task` 或 `TaskConfig` 增加可选字段：

```go
FlowTemplateID string `json:"flowTemplateId,omitempty"`
```

`handleCreateTask` 从 multipart 读取可选字段：

```text
flowTemplateId=<template id>
```

该字段只作为来源记录，不参与决定实际运行的 flow。实际运行 flow 仍来自 multipart 中的 `flow.json`，这样当前画布启动和模板启动走同一条后端路径。

归档时：

- `task_config_archive.flow_json` 保存完整实际运行快照。
- `task_history.flow_template_id` 保存来源模板 ID。

历史详情不反查 `flow_template.flow_json`。模板被覆盖或删除后，历史任务仍展示当时的真实运行配置。

## 前端 API 与流程管理

新增 `cmd/web/src/services/flowsApi.ts`，封装 `/sbot/flows`。`flowManagerStore.ts` 保留现有函数名以降低 UI 改动范围，但底层改为后端 API：

```ts
saveFlow(name, flow, layout, existingId?)
getFlow(id)
listFlows()
renameFlow(id, name)
deleteFlow(id)
```

`ManagedFlow` 使用后端时间字段，列表项不携带完整 `flow/layout`，详情读取时再请求 `GET /sbot/flows/{id}`。

`FlowManagerModal` 文案调整：

- 标题从「本地流程管理」改为「流程管理」或「流程模板」。
- 空态文案改为「暂无保存的流程」。
- 错误提示使用「服务器未启用流程库」等用户可理解文案，不暴露 Admin、IndexedDB 等技术词。

旧 IndexedDB 代码删除，不做 legacy reader。

## 启动弹窗选择流程

`TaskStartModal` 在任务名上方增加「流程来源」区域：

```text
流程来源：[当前画布] [已保存流程]
```

默认选择「当前画布」。选择「已保存流程」时显示下拉框，数据来自 `listFlows()`，选项展示名称、更新时间、节点数。点击启动时读取完整详情。

抽象统一函数：

```ts
async function getSelectedFlow(): Promise<{
  flow: FlowJson;
  layout?: FlowLayout;
  flowTemplateId?: string;
}>
```

当前画布来源：

- `flowStore.toTaskFlow()`
- `flowStore.layout`

已保存流程来源：

- `getFlow(id).flow`
- `getFlow(id).layout`
- `flowTemplateId = id`

## startTask 数据流重构

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

`startTask` 不再读取当前画布作为隐式来源，而是全程使用 `opts.flow`：

- 流程校验。
- 脚本引用检测与同步。
- codec 引用检测。
- share 脚本预检。
- multipart 上传 `flow.json`。
- 可选上传 `flowTemplateId`。

启动成功后调用共享函数：

```ts
stashAndReplaceCanvas(opts.flow, opts.flowLayout)
```

该函数负责：

1. 将当前编辑草稿 flow/layout stash 到 LocalStorage。
2. 用实际启动的 flow/layout 替换画布。
3. 清空旧监控数据和 metrics provider。

`attachToActive` 读取远端 `flow/flow.json` 后也复用该函数，确保主动启动与挂载运行任务两条路径一致。

## 资源关系

流程模板只保存 flow 与 layout，不保存 Lua、proto、adapter 或 errors.json。启动任务时仍沿用现有资源链路：

- 按实际启动 flow 收集引用脚本。
- proto 继续全量提交。
- adapter codec 继续全量提交，并校验实际启动 flow 引用的连接都存在对应 codec。
- `adapter/errors.json` 继续按现有逻辑提交。

这样流程模板不会复制资源内容，也不会制造资源版本漂移。模板完整性在启动前通过现有缺脚本、缺协议配置、共享状态预检来保证。

## 兼容性与数据边界

本阶段不做旧字段兼容和自动迁移。前端流程库切到后端后，旧浏览器本地流程不再展示。服务器未配置 MySQL 时，流程管理与已保存流程启动不可用，但当前画布启动仍可用。

`flowTemplateId` 只是来源标记。即使前端传入不存在的 ID，服务端也不使用它取 flow；实现时可以选择不强校验，或在 `flows != nil` 时校验存在并返回 `FLOW_TEMPLATE_NOT_FOUND`。推荐校验存在，避免历史溯源记录无效来源。

## 测试与验证

后端测试：

- `FlowTemplateStore` 的 Create/List/Get/Update/Delete。
- name 校验、not found、disabled 错误。
- `handleCreateTask` 读取 `flowTemplateId`。
- 历史归档写入 `task_history.flow_template_id`，且 `task_config_archive.flow_json` 仍保存实际 flow。

前端测试：

- `flowsApi` 基本请求适配。
- `flowManagerStore` 调用后端 API 的保存、读取、列表、重命名、删除。
- `TaskStartModal` 当前画布与已保存流程两种来源的参数组装。
- `startTask` 使用 `opts.flow`，而不是读取当前画布。

完整验证：

```text
go test ./...
go build ./...
cd cmd/web && npx tsc -b
cd cmd/web && npm run test
```

手动验证：

1. MySQL 未配置时，流程库接口返回 `FLOW_LIBRARY_DISABLED`，当前画布启动仍可用。
2. MySQL 配置后可创建、列表、打开、重命名、覆盖、删除流程模板。
3. 从当前画布启动任务仍正常。
4. 从已保存流程启动任务时，上传的 `flow.json` 是模板 flow。
5. 启动后运行态画布显示实际启动的模板流程。
6. 返回编辑模式时能恢复原草稿。
7. 任务终态后历史归档保留实际 flow 快照，并记录来源模板 ID。
