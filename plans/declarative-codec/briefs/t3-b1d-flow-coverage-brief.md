# T3 Batch-1 任务 D — 补完 §3.5：flow 引用连接的 codec 覆盖校验

> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）。
> 依赖：B1-A/B/C 已完成（多 codec 存储/上传/sync）。本任务补 B1-C 遗漏的 §3.5 另一半。
> **背景**：B1-C 只做了「至少有一份 codec」（`listCodecFiles()` 非空），没做计划 §3.5 要求的「flow 引用的
> 每条连接都有对应 codec 文件」。本任务补全，让任务启动前就 fail-loud 提示缺失的连接（而非等 agent
> dial 时才报错）。

## 1. 任务定位

启动任务时，flow 里所有 `tcp*/udp*` 动作引用的连接（`<proto>:<service>`）都必须在 IDB 有对应的
`<proto>_<service>_codec.json`，否则 agent 在该连接 dial 时 CodecResolver 解析不到 codec 会 fail-loud。
把该校验**前移到任务启动**，给清晰中文提示，是更好的 UX（启动前拦截 vs 启动后失败）。

## 2. 现状（先读码）

- `cmd/web/src/types/action.ts:7-14`：`ActionPattern`（tcp*/udp* 等）+ `PATTERNS_REQUIRE_SERVICE`（需 service
  的 pattern 集合，先 grep 定位它在哪定义——可能在 action.ts 或 refsCheck.ts）。
- `cmd/web/src/components/FlowEditor/validation/refsCheck.ts:369-371,405`：已有 `${proto}:${service}` 与
  proto 推导（`pattern.startsWith('tcp')→'tcp'` 思路）逻辑——**对齐复用**，勿另造。
- `cmd/web/src/services/taskActions.ts`（B1-C 已改）：`createTask` 里 `const codecs = await listCodecFiles();`
  + 空校验。本任务在此处加 flow 连接覆盖校验。
- `cmd/web/src/services/scriptSync.ts:39`：`collectFlowScriptNames(flow)` 是同类「flow→名字集合」纯函数，
  放新 helper 的参考。
- ResourcesDrawer（B1-B）有 `fileNameToConnName`（`<proto>_<service>_codec.json`→`<proto>:<service>`）本地
  实现——命名换算可考虑抽共享，但**本任务以最小改动为先**。

## 3. 实现规格

### 3.1 新增纯函数（可单测）

在 `cmd/web/src/services/taskResourceDiff.ts`（已是「flow→任务资源」模块）或 `scriptSync.ts` 新增并从
`services/index.ts` 导出：

```ts
/** 从 flow 抽取所有 tcp*/udp* 动作引用的连接集合（`<proto>:<service>`），去重、排序。 */
export function collectFlowCodecConnections(flow: FlowJson): string[];
/** 连接名 ↔ codec 文件名换算：'tcp:logic' ↔ 'tcp_logic_codec.json'。 */
export function connNameToCodecFileName(conn: string): string;   // 'tcp:logic' -> 'tcp_logic_codec.json'
export function codecFileNameToConnName(name: string): string;   // 反向
```

- `collectFlowCodecConnections`：遍历 `flow.actions`，对 pattern 以 `tcp`/`udp` 开头且 `service` 非空的动作，
  `proto = pattern.startsWith('tcp') ? 'tcp' : 'udp'`，收集 `${proto}:${service}`，去重排序返回。
  （心跳动作 tcpHeartbeat/udpHeartbeat 是 §3.6 Batch-4 才加的 pattern，本任务不强求覆盖——但若
  `ActionPattern` 类型已含则自然覆盖；用 pattern 前缀判断即可前瞻兼容。）
- 命名换算：与 ResourcesDrawer 的 `fileNameToConnName` 一致（去 `_codec.json` 后缀、首个 `_` 换 `:`；
  反向首个 `:` 换 `_` + `_codec.json`）。

### 3.2 taskActions.createTask 加覆盖校验

在现有 `codecs.length === 0 → 抛「缺少协议配置」`之后，加：

```ts
const codecFileNames = new Set(codecs.map((f) => f.name));
const referenced = collectFlowCodecConnections(flowJson);
const missing = referenced.filter((conn) => !codecFileNames.has(connNameToCodecFileName(conn)));
if (missing.length > 0) {
  throw new ApiError({
    code: 'INVALID_ARGUMENT',
    message: `以下连接缺少协议配置文件：${missing.join('，')}。请在「协议配置」面板新建对应连接的 codec。`,
    details: { missingCodecConnections: missing },
  }, 400);
}
```

（上传仍发**全部** codec 文件——agent resolver 加载全部；本任务只加「引用连接都有文件」的**校验**，不改上传范围。）

### 3.3 测试（TDD）

- `collectFlowCodecConnections`：构造含 tcpConnect(logic)/tcpRequest(battle)/udpSend(battle)/setState/lua
  等动作的 flow，断言返回 `['tcp:battle','tcp:logic','udp:battle']`（去重排序，排除无 service 与非 tcp/udp）。
- `connNameToCodecFileName`/`codecFileNameToConnName`：round-trip 几例（含 `udp:battle`）。
- 覆盖校验：抽一个纯函数 `findMissingCodecConnections(referenced: string[], codecFileNames: string[]): string[]`
  供单测（taskActions 内部调它），断言：referenced 全有→[]；缺 `udp:battle`→`['udp:battle']`。
- 上述纯函数单测放 `cmd/web/src/services/__tests__/`（新文件或并入既有）。

## 4. 全局约束（bind）

- **改动文件**：`taskActions.ts`（加校验）+ 放新 helper 的 service 文件（taskResourceDiff.ts 或 scriptSync.ts）
  + `services/index.ts`（导出）+ 新测试。**严禁动** B1-B 的 4 UI 文件、resourcesStore/baselineApi（除非抽共享
  命名换算需微调——优先不改 ResourcesDrawer，本地保留其 `fileNameToConnName`，本任务的换算函数独立实现一致逻辑即可）。
- **禁止兼容性兜底**：缺失即 fail-loud 中文，不静默、不 `??` 兜。
- **UI 文本不暴露技术术语**：错误文案用「连接」「协议配置」，不出现 codec/schema（变量名可保留）。
- **不要 git commit**。

## 5. 验证（全绿才算 DONE）

- `cd cmd/web && npx tsc -b` exit 0。
- `cd cmd/web && npm run test` 通过（新增 collectFlowCodecConnections/换算/覆盖校验的纯函数测试）。
- 自查 `git diff --stat`：改动限于上述文件。

## 6. 报告

写到 `plans/declarative-codec/reports/t3-b1d-flow-coverage-report.md`：实现要点、新 helper、taskActions
校验接入、测试用例、`tsc`/`test` 结果、自审、贴 `git diff --stat`。

返回消息只含：① 状态；② 改动文件清单；③ 一行测试摘要；④ concerns。有歧义先问。
