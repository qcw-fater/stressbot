# B1-2 报告：EditorPage 挂协议配置浮窗 + RuntimeBar 入口

## 状态
DONE

## 实现要点
- 把 B1-1 抽出的 `ProtocolConfigEditor` 挂到独立可拖拽缩放浮窗（复用 `FloatingWindow`）。
- RuntimeBar 「资源」按钮旁加「协议」按钮，并把 `codecSchemaErrors` 校验错误 Badge 从「资源」迁移到「协议」（语义更贴切）。
- 不改 ProtocolConfigEditor 内部 / 样式 / codecEditor。

## EditorPage 怎么挂的
严格仿 notepad 黄金参考模式（lazy import + useState + LazyMount + FloatingWindow + Suspense + Spin）。

lazy import（`cmd/web/src/pages/EditorPage.tsx`，在 notepad 之后、ActiveTaskGuardModal 之前）：
```tsx
const LazyProtocolConfigEditor = lazy(() =>
  import('@/components/modules/ProtocolConfigEditor').then((m) => ({ default: m.ProtocolConfigEditor })),
);
```

state（:128-133 抽屉区块）：
```tsx
const [protocolConfigOpen, setProtocolConfigOpen] = useState(false);
```

RuntimeBar prop 注入：
```tsx
<RuntimeBar
  ...
  onOpenNotepad={() => setNotepadOpen(true)}
  onOpenProtocolConfig={() => setProtocolConfigOpen(true)}
/>
```

浮窗挂载（notepad LazyMount 之后、guardTask 之前）：
```tsx
<LazyMount visible={protocolConfigOpen}>
  <FloatingWindow
    windowId="protocolConfig"
    title="协议配置"
    defaultSize={{ width: 900, height: 640 }}
    minSize={{ width: 600, height: 400 }}
    open={protocolConfigOpen}
    onClose={() => setProtocolConfigOpen(false)}
  >
    <Suspense fallback={<Spin />}>
      <LazyProtocolConfigEditor />
    </Suspense>
  </FloatingWindow>
</LazyMount>
```
FloatingWindow / Suspense / Spin / LazyMount 全部已在 EditorPage 现有 import 中（notepad / logs / systemStatus 同款）。

## RuntimeBar 按钮怎么加的

### 图标选择依据
grep 确认 `ClusterOutlined` 全仓零使用（`DeploymentUnitOutlined` 已被 `Toolbar.tsx:17,328` 占用）。按任务建议选用 `ClusterOutlined`（语义：多份连接 codec 集群式管理），避撞 ApiOutlined/SettingOutlined。

### Badge 决策（关键）
**决策：把 `codecSchemaErrors` Badge 从「资源」按钮迁移到「协议」按钮。** 「资源」按钮只保留 `pendingSyncResult`（同步冲突/移除）橙色 Badge。

**决策理由：**
1. `codecSchemaErrors` 是协议配置（codec.json 帧布局/错误码）的结构化校验错误，由 `ProtocolConfigEditor` 的校验逻辑产生（见 editorStore.setCodecSchemaErrors）。
2. 「协议」按钮入口正是 `ProtocolConfigEditor`，Badge 在这里出现，用户点击即跳到产生错误的编辑器，路径最短、语义最准。
3. 原来挂在「资源」上是历史包袱（B1-1 之前 AdapterTab 还在 ResourcesDrawer 里）。B1-1 已抽离，协议配置语义独立，Badge 应当跟随。
4. 「资源」按钮的同步冲突 Badge（`pendingSyncResult.conflicts + removed`，橙色）保留——同步是资源管理器的职责，与协议配置无关。

**Tooltip 文案调整：**
- 「协议」Tooltip：错误时 `协议配置有 N 处问题`；正常时 `协议配置：按连接管理帧布局与错误码映射`。
- 「资源」Tooltip：从「资源管理（proto / lua / 协议配置）」改为 `资源管理（proto / lua / 适配器）：编辑定义文件并下发到节点`（移除「协议配置」，因为它已独立）。

### 代码（RuntimeBar.tsx 按钮组）
```tsx
<Tooltip title={codecSchemaErrors && codecSchemaErrors.length > 0 ? `协议配置有 ${codecSchemaErrors.length} 处问题` : '协议配置：按连接管理帧布局与错误码映射'}>
  <Badge
    count={codecSchemaErrors && codecSchemaErrors.length > 0 ? codecSchemaErrors.length : 0}
    overflowCount={99}
    offset={[-4, 4]}
  >
    <Button icon={<ClusterOutlined />} onClick={onOpenProtocolConfig}>
      协议
    </Button>
  </Badge>
</Tooltip>
<Tooltip title="资源管理（proto / lua / 适配器）：编辑定义文件并下发到节点">
  <Badge
    count={pendingSyncResult ? pendingSyncResult.conflicts.length + pendingSyncResult.removed.length : 0}
    overflowCount={99}
    offset={[-4, 4]}
    color="orange"
  >
    <Button icon={<DatabaseOutlined />} onClick={onOpenResources}>
      资源
    </Button>
  </Badge>
</Tooltip>
```
Props interface 加 `onOpenProtocolConfig?: () => void;`，函数解构加同名参数。

## floatingWindowStore 注册
**未改动。** 任务规格写「DEFAULT_SIZES 加 protocolConfig」，但实读后发现规格对文件位置判断有误：

- `floatingWindowStore.ts` 没有 DEFAULT_SIZES 常量。
- `DEFAULT_SIZES` 在 `editorStore.ts:251`，且只服务于 `setActivePanel`（React Flow 画布内嵌面板：nodeEdit/listenEdit/protoBrowser/listenPanel/jsonPreview/templateEdit）。
- EditorPage 三个并列浮窗（`notepad` / `logs` / `systemStatus`）**均未**在 DEFAULT_SIZES 注册，而是直接在 JSX 把 `defaultSize`/`minSize` inline 传给 `FloatingWindow`（见 EditorPage:345-385）。

合规红线「FloatingWindow 用法严格仿 notepad 挂载」优先级高于规格的字面指令——notepad 挂载本身就是 inline `defaultSize`，不走 DEFAULT_SIZES。因此 protocolConfig 照 notepad 走 inline，保持 3 个并列浮窗的一致模式，不改 editorStore（editorStore 的 DEFAULT_SIZES 服务于另一个分层，混入会破坏关注点分离）。

## tsc + test 结果
- `cd cmd/web && npx tsc -b`：EXIT 0。
- `cd cmd/web && npm run test`：22 files / 287 tests passed，0 failed。无回归。

## 自审
- ✅ `export function ProtocolConfigEditor`（非 default）—— lazy import 用 `.then(m => ({ default: m.ProtocolConfigEditor }))` 适配。
- ✅ Props 用 `interface RuntimeBarProps`。
- ✅ FloatingWindow 用法严格仿 notepad（windowId / open / onClose / defaultSize / minSize），无自设 zIndex。
- ✅ UI 文本「协议」「协议配置」，不暴露 codec / adapter 技术术语（「资源」Tooltip 原来的「协议配置」字样也改掉了）。
- ✅ 未碰 ProtocolConfigEditor.tsx / codecEditor/ / services/ / conf/。
- ✅ 未 git add / git commit。
- ✅ 颜色本任务不涉及（沿用 Badge 默认红 / orange）。

## git diff --stat（B1-2 改动）
```
 cmd/web/src/components/runtime/RuntimeBar.tsx | 26 +++++++++++++++++---------
 cmd/web/src/pages/EditorPage.tsx              | 19 +++++++++++++++++++
 2 files changed, 36 insertions(+), 9 deletions(-)
```

floatingWindowStore.ts：0 行改动（理由见上）。

### 工作区其它 dirty（非 B1-2 引入，本次未触碰）
会话开始时即已存在：
- `cmd/web/src/components/modules/ResourcesDrawer.tsx`（B1-1 抽离后的瘦身）
- `conf/scripts/ranked_*.lua` × 5（MEMORY 提到的 stash@{0} 排位迁移重做残留）
- `docs/2026-06-18-flow-template-backend-config-design.md`

本次 B1-2 仅触碰 EditorPage.tsx 与 RuntimeBar.tsx 两个文件。
