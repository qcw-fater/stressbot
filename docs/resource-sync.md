# 资源同步 -- 显式拉取与隐式提交技术文档

## 1. 概述

stressbot 的 proto / lua / adapter 等资源，存在两份副本：

- **本地工作副本**：浏览器 IndexedDB，用户在「资源管理」面板和动作编辑器中编辑。
- **服务器基线**：Admin 进程磁盘上的 `conf/proto`、`conf/scripts`、`conf/adapter` 目录。Agent 执行任务时下载的就是这份基线。

资源同步借鉴 SVN/Git 工作副本（working copy）模型来协调这两份副本：每个本地文件记录一个 `baseHash`（相当于 SVN 的 pristine/BASE 拷贝哈希），据此做"本地 / 基线 / 服务器"三方判断，只有双方都改且内容不同才需要用户处理冲突。

### 1.1 核心心智模型

经过演进，最终确定为两个动作、一个心智：

| 动作 | 方向 | 触发方式 | 类比 |
|------|------|----------|------|
| **拉取** | 服务器 → 本地 | 显式（「资源管理」面板的「拉取」按钮） | `svn update` |
| **提交** | 本地 → 服务器 | 隐式（启动任务时随配置写盘） | `svn commit` |

设计要点：

- **加载 / 刷新页面不自动拉取**：避免无感覆盖用户的本地编辑稿。资源拉取只在用户主动点击「拉取」时发生。
- **没有独立的「推送」按钮**：在压测工具里，"把本地资源持久化到服务器"这件事的自然时机就是"我真的要用这些资源去跑压测"。启动任务本身已经把配置写回 `conf/` 基线，所以不需要额外的主动推送动作。
- **冲突由专门的对比面板（`BaselineSyncModal`）显式处理**：用户逐项选择保留本地还是采用服务器版本。

### 1.2 演进说明

该功能早期版本会在页面加载/刷新/加载流程时自动全量拉取，并提供过独立的「推送」按钮 + svn status 状态视图。后续因为以下原因简化：

- 自动拉取会无感覆盖本地编辑稿，违背"用户主导"的预期。
- 独立推送在本工具里需求很弱（启动任务已隐式提交），且带来全量覆盖、无删除语义、后端部分写失败处理等复杂度。

因此最终回归到"显式拉取 + 启动任务隐式提交"的纯净模型。本文档描述的是当前实现。

---

## 2. 三方工作副本模型

### 2.1 本地存储结构

资源以 utf-8 字符串存于 IndexedDB。受 `idb-keyval` 限制（每个 DB 只挂一个 object store），proto / scripts / adapter 各用一个独立 DB：

| DB 名 | 内容 |
|-------|------|
| `stressbot-resources-proto` | 所有 `.proto` 文件 |
| `stressbot-resources-scripts` | 所有 `.lua` 脚本 |
| `stressbot-resources-adapter` | `codec.lua` + `error.lua` |

每个文件的数据结构（`[cmd/web/src/services/resourcesStore.ts](../cmd/web/src/services/resourcesStore.ts)`）：

```ts
interface ResourceFile {
  name: string;
  content: string;
  size: number;
  uploadedAt: string;
  /** 上次确认同步到的服务器内容 hash；null 表示确认时服务器没有该资源。 */
  baseHash?: string | null;
}
```

哈希用 `SHA-256`，格式 `sha256:<hex>`。

### 2.2 三个哈希

任意时刻一个文件可以算出三个哈希，三方比较即由它们驱动：

| 名称 | 含义 |
|------|------|
| `localHash` | 当前本地内容的哈希 |
| `serverHash` | 当前服务器基线内容的哈希（服务器没有该文件时为 `null`） |
| `baseHash` | 上次"确认同步点"时的服务器内容哈希（存在 `ResourceFile` 上） |

`baseHash` 的三种取值语义：

- **某个哈希值**：上次同步时服务器有该文件，且内容哈希为此值。
- **`null`**：上次同步时服务器没有该文件（本地新增、尚未提交）。
- **`undefined`**：从未经历过同步确认（早期数据 / 直接上传未走基线路径），历史未知。

### 2.3 baseHash 的写入时机

`baseHash` 只在"确认与服务器一致"的时刻被设置：

| 写入函数 | 场景 | baseHash 取值 |
|----------|------|---------------|
| `serverResourceFile`（`addProtoFromBaseline` 等） | 从基线拉回 / 采用服务器版本写入本地 | 服务器内容哈希 |
| `localResourceFile`（`addProto` / `addScript` / `setAdapterScript` 等） | 用户本地新增 / 编辑 | 沿用 previous 的 baseHash（无则 `null`） |
| `markResourcesAsBaselineSynced` | 启动任务提交成功后 | 刚提交内容的哈希 |
| `setResourceBaseHash` | reconcile 内部修正（legacyRepair / 冲突解决保留本地） | 视场景而定 |

---

## 3. 同步方向与触发时机

```
                    拉取（显式）
   服务器基线 conf/  ─────────────►  本地工作副本 IndexedDB
   (Admin 磁盘)                          (浏览器)
        ▲                                   │
        │           提交（启动任务隐式）       │ 编辑
        └───────────────────────────────────┘
```

| 触发点 | 行为 | 文件 |
|--------|------|------|
| 页面加载 / 刷新 | **不拉取**；仅做适配器必需函数校验 `validateAdapter` | `[cmd/web/src/components/FlowEditor/index.tsx](../cmd/web/src/components/FlowEditor/index.tsx)` |
| 加载 / 导入 flow | 仅做脚本 gap-fill（见 §6），**不做全量基线对比** | `[cmd/web/src/components/FlowEditor/panels/Toolbar.tsx](../cmd/web/src/components/FlowEditor/panels/Toolbar.tsx)` |
| 点击「拉取」按钮 | `syncResourcesFromBaseline()`，冲突弹 `BaselineSyncModal` | `[cmd/web/src/components/modules/ResourcesDrawer.tsx](../cmd/web/src/components/modules/ResourcesDrawer.tsx)` |
| 启动任务 | `createTask` 上传配置 → 后端 `writeBaselineFiles` 写盘提交 | `[cmd/web/src/services/taskActions.ts](../cmd/web/src/services/taskActions.ts)` |
| 启动任务（提交前） | `checkTaskResourcesAgainstBaseline` 做一次差异检查，有冲突弹二选一 | `[cmd/web/src/components/runtime/TaskStartModal.tsx](../cmd/web/src/components/runtime/TaskStartModal.tsx)` |

---

## 4. 拉取（svn update）

### 4.1 入口与编排

「资源管理」面板顶部的「拉取」按钮调用 `syncResourcesFromBaseline()`：

```
fetch proto/index.json + scripts/index.json + adapter/codec.lua + adapter/error.lua
  → 对 proto / scripts 分组做 syncFileGroup
  → 对 codec.lua / error.lua 单独 reconcile
  → 汇总 BaselineSyncResult { added, unchanged, conflicts, removed }
```

返回结果分类：

| 字段 | 含义 | 处理 |
|------|------|------|
| `added` | 基线有、本地没有 | 已自动写入本地，提示"新增 N 个资源" |
| `unchanged` | 双方一致 | 无操作 |
| `conflicts` | 双方都改且内容不同 | 弹 `BaselineSyncModal` 让用户逐项确认 |
| `removed` | 本地有、服务器删除且本地也改过 | 弹 `BaselineSyncModal` 确认删除还是保留 |

### 4.2 三方判定算法

`compareResourceThreeWay(local, serverContent)` 输出 7 种 `ThreeWayKind`：

| kind | 条件 | reconcile 行为 |
|------|------|----------------|
| `unchanged` | localHash == serverHash 且 baseHash == serverHash | 不动 |
| `legacyRepair` | localHash == serverHash 但 baseHash 不等（历史未对齐） | 修正 baseHash 为 serverHash，视作已同步 |
| `localOnlyChanged` | 仅本地改（baseHash == serverHash，或服务器无且 baseHash==null） | **保留本地**，不提示 |
| `serverOnlyChanged` | 仅服务器改（baseHash == localHash） | **自动采用服务器版本**写入本地 |
| `serverRemovedOnly` | 服务器删除、本地未改（baseHash == localHash） | 自动删除本地 |
| `conflict` | 双方都改且内容不同 / baseHash 未知且双方内容不同 | 进 `conflicts`，需用户处理 |
| `removedConflict` | 服务器删除但本地已改 / baseHash 未知且服务器无 | 进 `removed`，需用户处理 |

判定流程（`[cmd/web/src/services/resourcesStore.ts](../cmd/web/src/services/resourcesStore.ts)` `compareResourceThreeWay`）：

```
localHash == serverHash ?
  └ yes → baseHash == serverHash ? unchanged : legacyRepair
  └ no  → baseHash === undefined ? (serverHash==null ? removedConflict : conflict)
          serverHash == null ?
            └ baseHash==null → localOnlyChanged
            └ baseHash==localHash → serverRemovedOnly  否则 removedConflict
          baseHash == serverHash → localOnlyChanged
          baseHash == localHash  → serverOnlyChanged
          否则 → conflict
```

要点：`serverOnlyChanged` / `serverRemovedOnly` 会**自动合并**（这是 svn update 的正确语义）。因为拉取已是用户主动动作，自动合并可接受，不会出现"刷新页面被静默覆盖"的问题。

---

## 5. 冲突解决（BaselineSyncModal）

`[cmd/web/src/components/modules/BaselineSyncModal.tsx](../cmd/web/src/components/modules/BaselineSyncModal.tsx)`：

- 用 Monaco `DiffEditor` 逐项展示"本地 vs 服务器"差异，左右对照。
- 每项二选一：`保留本地` / `采用服务器`（`removed` 项的"采用服务器"即"删除本地"）。
- 支持批量"全部保留本地 / 全部采用服务器"、上一项/下一项导航、底部圆点状态指示。
- 性能：同一时刻只渲染一个 `DiffEditor` 实例，切换时清空上一个 model，避免冲突项过多时内存溢出。

用户确认后调用 `applyConflictResolution(decisions)`：

| 决策 | 服务器有该文件 | 行为 |
|------|----------------|------|
| `keepLocal=true` | 是/否 | 把 baseHash 刷成服务器内容哈希（或 null），本地内容不变 → 下次该项变成 `localOnlyChanged`，不再报冲突 |
| `keepLocal=false` | 是 | 用服务器内容覆盖本地 |
| `keepLocal=false` | 否 | 删除本地文件 |

未处理的冲突结果保存在 `editorStore.pendingSyncResult`，「资源管理」面板顶部和工具栏「资源」按钮角标会提示待处理冲突数。

---

## 6. 脚本 gap-fill（让流程能跑起来）

`[cmd/web/src/services/scriptSync.ts](../cmd/web/src/services/scriptSync.ts)` 的 `syncFlowScriptsToIdb(flow)` 是与"拉取"**独立**的兜底机制：

- 扫描 flow 中被引用的 lua 脚本名（`actions[].script`、`listens[].script`、`condition`/`breakCondition` 的 `lua:` 前缀）。
- 对本地**完全没有**的脚本，从基线 `/sbot/baseline/scripts/<name>` 拉回写入 IndexedDB。
- **不覆盖**本地已存在的脚本（保护用户编辑稿）。

它属于"让引用到的脚本存在、流程能启动"的必要补齐，不是与服务器对比合并，因此在加载 flow、打开启动弹窗、提交任务前都会执行。基线更新检测（覆盖合并）才由 `syncResourcesFromBaseline` 负责。

---

## 7. 提交（启动任务隐式 commit）

### 7.1 前端：组装并上传

`[cmd/web/src/services/taskActions.ts](../cmd/web/src/services/taskActions.ts)` 的 `startTask`：

1. 校验 flow（`validateFlow`），有 error 直接拒绝。
2. `syncFlowScriptsToIdb` gap-fill，仍缺失脚本则抛错。
3. 收集 IDB 资源：全量 proto、flow 引用到的 lua（`usedScripts`）、`codec.lua`、`error.lua`。
4. 组装 multipart（`flow.json` + `proto/<n>` + `scripts/<n>` + `adapter/codec.lua` + `adapter/error.lua`）。
5. `POST /sbot/tasks` 创建任务；成功后立即 `markResourcesAsBaselineSynced` 把上传内容的哈希写为新的 `baseHash`，避免下次误报冲突。
6. `POST /sbot/tasks/{id}/start` 启动。

> 注意提交范围：启动任务只写回**当前 flow 引用到的** lua 脚本，proto 全量、adapter 全量、flow 覆盖写。本地存了但当前流程没用到的脚本不会被提交。这对"编辑什么就跑什么"的常见场景已足够。

### 7.2 后端：写回基线

Admin 在创建任务时调用 `writeBaselineFiles`（`[admin/handlers.go](../admin/handlers.go)`），把上传的 flow / proto / scripts / adapter 写入 `conf/` 对应目录，使下次拉取时本地与基线一致：

```go
s.writeBaselineFiles(&cfg, flowData)   // 创建任务流程内调用
```

`safeWriteFile` 用 `filepath.Base` 防路径穿越，自动建目录。

### 7.3 启动弹窗的冲突处理

`[cmd/web/src/components/runtime/TaskStartModal.tsx](../cmd/web/src/components/runtime/TaskStartModal.tsx)` 在点击「启动」时（非打开弹窗时）做一次任务资源与基线的差异检查：

```
handleSubmit
  → syncFlowScriptsToIdb（gap-fill）
  → checkTaskResourcesAgainstBaseline(flow)
  → 有冲突 ? 弹二选一：
        ├ 逐项处理冲突（BaselineSyncModal）→ handleDiffResolved → executeStart
        └ 覆盖运行（全部用本地）→ handleOverwriteRun → executeStart
  → 无冲突 → executeStart（createTask 写盘提交 + startTask）
```

"覆盖运行"语义即"本次全部使用本地版本，然后随任务提交覆盖服务器基线"。

---

## 8. 后端基线读取端点

供前端拉取使用，全部为只读（`[admin/handlers.go](../admin/handlers.go)`）：

| 端点 | 说明 |
|------|------|
| `GET /sbot/baseline/proto/index.json` | proto 文件名列表 |
| `GET /sbot/baseline/proto/{name}` | 指定 proto 内容 |
| `GET /sbot/baseline/scripts/index.json` | lua 脚本名列表 |
| `GET /sbot/baseline/scripts/{name}` | 指定脚本内容 |
| `GET /sbot/baseline/adapter/codec.lua` | 适配器内容 |
| `GET /sbot/baseline/adapter/error.lua` | 错误码映射内容 |
| `GET /sbot/baseline/flow/flow.json` | 基线流程 |
| `GET /sbot/baseline/config.json` | 基线运行配置 |

前端读取统一经过 `[cmd/web/src/services/baselineApi.ts](../cmd/web/src/services/baselineApi.ts)`，组件不直接 fetch。基线前缀由 `services/env.ts` 的 `BASELINE_PREFIX`（默认 `/sbot/baseline`）集中管理。

> 不存在"写基线"的独立 HTTP 端点。基线的写入只发生在创建任务时（`writeBaselineFiles`）。

---

## 9. 关键文件与函数

| 文件 | 职责 |
|------|------|
| `[cmd/web/src/services/resourcesStore.ts](../cmd/web/src/services/resourcesStore.ts)` | IndexedDB 资源 CRUD、三方比较、拉取编排、冲突应用 |
| `[cmd/web/src/services/baselineApi.ts](../cmd/web/src/services/baselineApi.ts)` | 基线只读 API（fetchBaseline* 系列） |
| `[cmd/web/src/services/scriptSync.ts](../cmd/web/src/services/scriptSync.ts)` | flow 引用脚本 gap-fill |
| `[cmd/web/src/services/taskResourceDiff.ts](../cmd/web/src/services/taskResourceDiff.ts)` | 任务资源与基线差异检查（启动前） |
| `[cmd/web/src/services/taskActions.ts](../cmd/web/src/services/taskActions.ts)` | 启动任务编排（隐式提交点） |
| `[cmd/web/src/components/modules/ResourcesDrawer.tsx](../cmd/web/src/components/modules/ResourcesDrawer.tsx)` | 资源管理面板（拉取按钮 + 三 Tab） |
| `[cmd/web/src/components/modules/BaselineSyncModal.tsx](../cmd/web/src/components/modules/BaselineSyncModal.tsx)` | 冲突解决对比面板 |
| `[admin/handlers.go](../admin/handlers.go)` | 基线读取端点 + 创建任务时写盘 `writeBaselineFiles` |

核心函数速查（resourcesStore）：

| 函数 | 作用 |
|------|------|
| `hashResourceContent` | SHA-256 计算 |
| `compareResourceThreeWay` | 三方判定，返回 `ThreeWayKind` |
| `reconcileResourceWithServer` | 按判定结果自动合并 / 收集冲突 |
| `syncResourcesFromBaseline` | 拉取主入口 |
| `applyConflictResolution` | 应用用户冲突决策 |
| `markResourcesAsBaselineSynced` | 提交成功后刷新 baseHash |
| `subscribe` / `notify` | 资源变更订阅（供 React 组件刷新列表） |

---

## 10. 设计取舍 / FAQ

**Q：为什么加载/刷新页面不自动拉取？**
A：自动拉取会触发 `serverOnlyChanged` 的静默覆盖，在用户无感时刻改动其本地编辑稿，体验上像"我的东西自己变了"。改为显式拉取后，覆盖只发生在用户主动确认的时刻。

**Q：为什么没有独立的「推送」按钮？**
A：在压测工具里，把本地资源持久化到服务器的自然时机就是"启动任务"，而启动任务已经通过 `writeBaselineFiles` 隐式提交。独立推送的额外价值只有"不跑任务也想存到服务器"，需求很弱，却要引入全量覆盖、无删除语义、部分写失败处理等复杂度，因此去掉。

**Q：本地删除一个文件，能同步到服务器吗？**
A：不能。当前没有删除传播（svn delete）语义——提交只写文件、不删服务器文件。如需让服务器某文件消失，需在 `conf/` 直接删除。

**Q：`baseHash` 为 `undefined` 的历史数据怎么办？**
A：拉取时若发现 `localHash == serverHash` 但 baseHash 不一致/未知，会走 `legacyRepair` 自动把 baseHash 修正为 serverHash，后续即正常参与三方判断。

**Q：冲突里选了"保留本地"，下次还会再弹吗？**
A：不会。`applyConflictResolution` 在 keepLocal 时把 baseHash 刷成服务器当前内容哈希，该项随后变为 `localOnlyChanged`（仅本地改），拉取时保留本地、不再提示。
