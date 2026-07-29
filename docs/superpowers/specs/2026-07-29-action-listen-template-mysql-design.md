# Action/Listen 模板库 MySQL 存储设计

## 目标

把流程编辑器中的 Action 模板库和 Listen 模板库从浏览器本地存储迁移到 Admin 全局 MySQL，使连接到同一服务器的用户共享同一套模板，并让模板库能够参与现有的配置备份、合并导入、完整恢复和失败回滚。

首版行为与现有流程模板库保持一致：普通单条编辑不增加乐观锁，后写入的内容生效；整库快照操作使用版本校验和事务，避免在用户确认恢复期间覆盖其他修改。

## 非目标

- 不增加用户、租户或权限隔离。同一 Admin 的所有用户共享并可修改模板库。
- 不增加 WebSocket、SSE 或轮询推送。其他浏览器的修改在重新进入模板库、窗口重新聚焦或手动刷新时可见。
- 不在 MySQL 不可用时回退到 IndexedDB，避免形成两个事实来源。
- 不自动扫描、上传或引导迁移浏览器中的旧模板。旧 IndexedDB 数据不删除，但新版本不再读取。
- 不为单条模板增加 `revision`、ETag 或编辑锁。
- 不在保存模板时校验具体 Proto、Lua 文件是否存在；模板允许跨环境复用，也允许保存尚未补齐资源依赖的草稿。

## 已确认决策

1. Action 和 Listen 使用服务器级共享模板库。
2. 仅在全局 MySQL 可用时启用；不可用时相关入口禁用。
3. 旧本地模板不自动迁移。需要保留时由用户在升级前从旧版本导出，升级后手动导入。
4. Action 名称在 Action 库内唯一，Listen 名称在 Listen 库内唯一；两类模板可以同名。
5. 名称使用当前精确匹配语义，区分大小写。
6. 单条更新与流程模板一致，采用按 ID 覆盖的后写入生效语义。
7. 多浏览器之间不实时推送，通过进入页面、窗口聚焦或手动操作刷新。
8. 数据库采用两张独立表，而不是通用类型表或整库 JSON 文档。
9. Action、Listen 各自拥有独立快照版本和事务边界。

## 总体架构

后端新增 `ActionTemplateStore` 与 `ListenTemplateStore`。两者由 `AdminServer` 注入同一个全局 `*sql.DB`，各自持有进程内 `sync.RWMutex`，并暴露普通 CRUD 与整库快照能力。MySQL 负责持久化、唯一约束和事务；进程内锁让当前单 Admin 部署中的普通 CRUD 与快照替换不会交错。

前端保留 `templateStore.ts` 作为模板库门面，维持现有组件所依赖的方法和 `template-change` 通知。门面不再访问 `idb-keyval`，而是调用 `services/api.ts` 中集中定义的请求函数。流程画布、保存按钮、编辑抽屉和节点面板不直接发起 HTTP 请求。

现有配置传输模块继续拥有备份文件编解码、冲突规划和跨分区恢复协调职责。Action、Listen 分区从本地分区改成带版本的服务器分区，通过各自快照接口读取、应用和回滚。

## 数据模型

### Action 模板表

```sql
CREATE TABLE IF NOT EXISTS action_template (
    id           VARCHAR(32)  NOT NULL PRIMARY KEY,
    name         VARCHAR(80)  CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    description  VARCHAR(500) NULL,
    pattern      VARCHAR(32)  NOT NULL,
    data_json    MEDIUMBLOB   NOT NULL,
    created_at   DATETIME(3)  NOT NULL,
    updated_at   DATETIME(3)  NOT NULL,
    UNIQUE INDEX uq_action_template_name (name),
    INDEX idx_action_template_updated (updated_at DESC)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

### Listen 模板表

```sql
CREATE TABLE IF NOT EXISTS listen_template (
    id                VARCHAR(32)  NOT NULL PRIMARY KEY,
    name              VARCHAR(80)  CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    description       VARCHAR(500) NULL,
    kind              VARCHAR(32)  NOT NULL,
    data_json         MEDIUMBLOB   NOT NULL,
    default_ref_json  MEDIUMBLOB   NULL,
    created_at        DATETIME(3)  NOT NULL,
    updated_at        DATETIME(3)  NOT NULL,
    UNIQUE INDEX uq_listen_template_name (name),
    INDEX idx_listen_template_updated (updated_at DESC)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

两类表使用逻辑关联，不增加外键。模板被应用到流程时复制其定义，删除模板不会级联修改现有流程。

`pattern` 和 `kind` 作为列表展示元数据单独存储，完整模板定义保存在 `data_json`。写入时后端校验元数据与 JSON 内容形态一致，避免二者漂移。

## API 契约

### 普通 CRUD

```text
GET    /sbot/action-templates
POST   /sbot/action-templates
GET    /sbot/action-templates/{id}
PUT    /sbot/action-templates/{id}
DELETE /sbot/action-templates/{id}

GET    /sbot/listen-templates
POST   /sbot/listen-templates
GET    /sbot/listen-templates/{id}
PUT    /sbot/listen-templates/{id}
DELETE /sbot/listen-templates/{id}
```

列表接口返回完整模板，避免节点面板拖入模板时再产生逐项请求。首版与流程模板一致，不分页，按 `updatedAt` 倒序返回。

创建请求不接受 ID 和时间，服务器使用现有 `generateID()` 规则生成 ID，并写入创建、更新时间。更新按路径 ID 定位并整体覆盖可编辑字段，保留原创建时间。不存在的 ID 返回模板不存在，不执行 upsert。

### 快照接口

```text
GET /sbot/action-templates/snapshot
PUT /sbot/action-templates/snapshot

GET /sbot/listen-templates/snapshot
PUT /sbot/listen-templates/snapshot
```

读取响应：

```json
{
  "revision": "sha256:...",
  "items": []
}
```

替换请求：

```json
{
  "expectedRevision": "sha256:...",
  "idPolicy": "preserve",
  "items": []
}
```

`idPolicy` 只有两种值：

- `preserve`：用于完整恢复和回滚。所有模板必须带合法且唯一的 ID，服务端原样保留 ID 与时间信息。
- `generate-missing`：用于合并导入。已有目标项携带目标 ID；新增项不携带 ID，由服务端在事务中生成。导入文件中的来源 ID 不参与新增项写入。已有目标项保留原创建时间，内容发生变化时由服务端把更新时间设为当前时间；新增项的创建、更新时间也由服务端生成。

服务端按 ID 稳定排序，对 ID、名称、描述、模板内容、默认注册信息和时间的规范化表示计算快照 revision。`PUT` 在写锁和一个 InnoDB 事务内完成以下步骤：读取当前快照、校验 `expectedRevision`、按 `idPolicy` 规范化 ID 和时间、校验最终列表、删除旧列表、批量插入最终列表、提交并返回新 revision 与最终列表。任一步失败都回滚，不产生部分模板库。

普通 CRUD 不校验 revision。快照 revision 只保护批量导入、完整恢复和补偿回滚。

### 能力信息

`GET /sbot/capabilities` 增加：

```json
{
  "templateLibrary": true
}
```

Action 与 Listen 共用全局 MySQL，因此只需要一个能力字段。未配置 MySQL 时该值为 `false`，相关接口统一返回“服务器未启用模板库”。

## 后端校验

所有普通写入和快照写入复用相同校验函数：

- 名称去除首尾空格后不能为空，最长 80 个字符。
- 描述最长 500 个字符。
- Action `pattern` 必须属于当前 14 种动作，并与 `data.pattern` 相同。
- Listen `kind` 必须是 `silent`、`declarative` 或 `lua`，并与原始 JSON 中的字段存在形态一致。
- `data_json` 必须是 JSON 对象，并能解析成 `engine.ActionDef` 或 `engine.ListenDef`。
- `defaultRef` 存在时必须是 JSON 对象，并校验 `server`、`route` 和可选 `queueSize` 的基础结构。
- 快照内不允许重复 ID 或同类重复名称。
- 单个请求和快照请求使用明确的请求体上限，快照上限与现有配置恢复的 50 MiB 限制一致。

校验只检查结构和模板自身不变量，不要求模板当前可以直接执行。例如 declarative Listen 可以暂时保存空的 `s2cProto`，Action 也不要求服务器当前拥有其引用的 Proto 或 Lua 文件。

## 并发语义

InnoDB 行锁和唯一索引保证单次 SQL 更新与名称唯一性，但不阻止两个用户基于旧内容先后保存。首版明确接受这种后写入覆盖语义，与当前流程模板 CRUD 保持一致。

整库操作不同：用户预检时记录快照 revision，确认后提交的 `PUT snapshot` 必须匹配该 revision。期间只要发生任何普通 CRUD 或其他快照写入，revision 就会变化，当前整库操作返回冲突并要求重新预检。

进程内锁只承诺单个 Admin 进程内的一致性。本设计不宣称支持多个 Admin 实例同时连接同一数据库进行无冲突快照替换，这与当前流程模板部署边界一致。

## 前端行为

### 模板库门面

`templateStore.ts` 保留以下职责：

- 提供 Action/Listen 的保存、更新、列表、删除和查名方法。
- 调用 `services/api.ts` 中的模板 API，不直接使用 `fetch`。
- 把 API 的时间字符串转换为现有组件需要的时间表示，或统一调整模板类型后由门面屏蔽差异。
- 成功变更后发送一次 `template-change` 事件。
- 请求失败时不发送成功事件，也不把内存中的临时内容当成已保存数据。

原有 IndexedDB store 和 `nanoid` 创建逻辑从正常模板操作路径移除。旧数据库不清理。

### 刷新与降级

- 打开节点面板或模板编辑入口时重新加载。
- 浏览器窗口从后台重新获得焦点时重新加载。
- 提供手动刷新操作。
- 当前页面成功增删改后立即刷新或更新列表。
- 不做实时推送；其他用户的变化在上述刷新点可见。

`templateLibrary=false` 时，保存为模板、模板编辑、删除、模板导入和导出入口禁用，并显示“服务器未启用模板库”。流程编辑本身仍然可用。

读取失败时保留当前页面已经显示的旧列表并提示刷新失败；写入失败时保留用户正在编辑的内容，允许修正或重试。

## 导入、导出与配置恢复

模板库自身的导入/导出和完整配置中的 `actionTemplates`、`listenTemplates` 分区都以 MySQL 为数据源，不再读取 IndexedDB。

### 重复判断

- Action 只按 Action 库中的精确名称判断重复。
- Listen 只按 Listen 库中的精确名称判断重复。
- 来源 ID 在合并导入时不是业务冲突键。

这条规则取代旧配置恢复设计中模板分区“ID 相同或名称相同”的判断。原因是跨服务器导入时来源 ID 没有用户语义，名称才相当于目标目录中的文件名。

### 合并导入

- 非重复项加入最终列表，但清除来源 ID，使用 `generate-missing` 让服务器生成新 ID。
- 覆盖同名项时保留目标 ID 和目标创建时间，替换描述、类型元数据、模板内容及默认注册信息，并更新修改时间。
- 忽略同名项时目标内容不变。
- 逐项处理继续兼容现有“保留两份”能力；副本使用现有唯一副本命名规则、清除来源 ID，并由服务器生成新 ID。
- 全部覆盖、全部忽略和逐项处理都先生成不可变最终列表，确认后一次性提交快照接口。

### 完整恢复

- 只替换用户选中的模板分区。
- 备份中不存在的目标模板被删除。
- 显式空分区会清空对应模板表。
- 使用 `idPolicy=preserve` 保留备份中的 ID、创建时间和更新时间。
- Action 和 Listen 分别提交，各自拥有独立 revision，互不影响。

### 恢复协调与回滚

配置恢复的分区适配器应从“仅流程分区带 revision”推广为通用的服务器版本分区能力。`flows`、`actionTemplates`、`listenTemplates` 都提供：

- 读取带 revision 的当前快照。
- 使用 expected revision 应用最终列表。
- 返回应用后的 revision。
- 使用应用后的 revision 执行补偿恢复。

恢复日志分别记录 Action、Listen 写入前快照、写入前 revision 和应用后 revision。后续分区失败时按现有逆序补偿机制恢复；如果模板库在应用后又被其他用户修改，补偿因 revision 不匹配而拒绝强行覆盖，并保留恢复日志供用户重试。

## 错误语义

新增或复用统一错误响应，至少区分：

- 模板库未启用：HTTP 503。
- Action/Listen 模板不存在：HTTP 404。
- 模板名称已存在：HTTP 409。
- 模板数据、名称或快照结构非法：HTTP 400。
- 快照 revision 已变化：HTTP 409。
- 数据库内部失败：HTTP 500。

前端根据稳定错误码显示中文提示。SQL、表名、驱动错误等内部细节只写服务器日志，不直接返回用户界面。

## 兼容与上线

- 两张表加入现有 `allDDL`，Admin 启动时使用 `CREATE TABLE IF NOT EXISTS` 初始化。
- 配置了 MySQL 的现有部署升级后得到空模板表，不自动导入任何浏览器数据。
- 未配置 MySQL 的部署仍可使用流程编辑器，但服务器模板库能力关闭。
- 旧 IndexedDB 数据保留在浏览器中，避免升级动作主动销毁用户数据；新版本不提供自动读取或迁移入口。
- 现有独立模板导出文件格式和完整配置备份格式继续可读；解码时把旧的毫秒时间戳规范化为 API 时间，缺少 `updatedAt` 时使用 `createdAt`，导入后由服务器生成合并新增项 ID。
- 新版导出的模板来自共享 MySQL，因此同一服务器上的所有用户看到相同导出内容。

## 测试与验收

### 后端

- DDL 创建两张 InnoDB 表，名称列使用二进制排序规则并分别唯一。
- Action、Listen 完整 CRUD，列表按更新时间倒序。
- 同类精确重名返回冲突，跨类别同名允许，大小写不同允许。
- 非法名称、描述、pattern、kind、JSON 和 defaultRef 被拒绝且不写入。
- 更新不存在 ID、删除不存在 ID 返回明确错误。
- 未配置 MySQL 时能力字段和所有模板接口正确降级。
- 快照 revision 对稳定排序后的相同内容保持一致。
- `preserve` 保留 ID/时间，`generate-missing` 只为缺失 ID 的项生成新 ID。
- 快照内任一非法项导致整个事务回滚。
- revision 不匹配时不写入并返回冲突。
- 普通 CRUD 与快照替换在单 Admin 进程内不会交错破坏最终列表。

### 前端

- API 类型、时间映射、错误码映射和模板门面行为正确。
- 成功变更只触发一次模板通知，失败不触发成功通知。
- 打开入口、窗口聚焦和手动刷新能够看到其他浏览器的修改。
- 能力关闭时相关入口禁用且没有 IndexedDB 回退。
- 同名预提示与数据库最终冲突提示都正确。
- 模板独立导入/导出读取和写入共享 MySQL。
- 合并导入的新增、覆盖、忽略、保留副本和逐项决策符合名称冲突规则。
- 完整恢复保留 ID，合并新增项使用服务器新 ID。
- Action、Listen 分区分别发生 revision 冲突时要求重新预检。
- 恢复中断后的逆序补偿、补偿冲突和持久恢复日志行为正确。

### 项目验证

1. `go build ./...`
2. 后端相关单元测试与 `go test ./...`
3. `cd cmd/web && npx.cmd tsc -b`
4. `cd cmd/web && npm.cmd run test`
5. 在流程编辑器打开恢复后的流程并确认校验报告无错误。
6. 使用两个浏览器窗口验证共享模板、非实时刷新和同名冲突。
7. 验证模板独立导入/导出、完整配置合并导入、完整恢复和故障回滚。
8. 按项目后端改动验证流程启动 Admin/Agent，并审查运行日志。

## 方案取舍

不使用单张通用模板表，因为 Action 与 Listen 字段、校验和独立恢复边界不同，类型字段会把差异转移到业务代码和唯一索引中。

不使用单行整库 JSON，因为单条编辑会重写整库，并使名称唯一性、查询、并发和局部恢复复杂化。

不为单条编辑增加乐观锁，因为流程模板当前也没有该语义。若多人编辑造成实际问题，应同时为流程、Action、Listen 三类模板设计一致的版本化编辑体验，而不是只为新模板表增加一套特殊行为。

最终方案以两张显式表承载领域差异，以相同的 CRUD 体验保持简单，以独立 revision 快照承载批量恢复所需的一致性。
