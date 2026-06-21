# T3 Batch-1 任务 D 报告 — 补完 §3.5：flow 引用连接的 codec 覆盖校验

> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）
> 需求源：`plans/declarative-codec/briefs/t3-b1d-flow-coverage-brief.md`
> 状态：**DONE**

## 1. 背景与目标

B1-C 只校验了「至少有一份 codec」（`listCodecFiles()` 非空），遗漏了计划 §3.5 的另一半：
flow 引用的**每条连接**（tcp*/udp* 动作的 `<proto>:<service>`）都必须在 IDB 有对应的
`<proto>_<service>_codec.json`。本任务把该校验**前移到任务启动**：缺失即 fail-loud 中文提示，
而不是等 agent dial 时 CodecResolver 解析不到 codec 才报错。

## 2. 实现要点

### 2.1 新增纯函数（`cmd/web/src/services/taskResourceDiff.ts`）

在文件头部常量区新增 codec 文件名后缀常量，并导出 4 个纯函数：

```ts
const CODEC_FILE_SUFFIX = '_codec.json';

/** 连接名 → codec 文件名：'tcp:logic' → 'tcp_logic_codec.json' */
export function connNameToCodecFileName(conn: string): string {
  return `${conn.replace(':', '_')}${CODEC_FILE_SUFFIX}`;
}

/** codec 文件名 → 连接名：'tcp_logic_codec.json' → 'tcp:logic' */
export function codecFileNameToConnName(name: string): string {
  const stripped = name.endsWith(CODEC_FILE_SUFFIX) ? name.slice(0, -CODEC_FILE_SUFFIX.length) : name;
  const idx = stripped.indexOf('_');
  if (idx < 0) return stripped;
  return `${stripped.slice(0, idx)}:${stripped.slice(idx + 1)}`;
}

/** 从 flow 抽取所有 tcp/udp 动作引用的连接集合（`<proto>:<service>`），去重、排序。 */
export function collectFlowCodecConnections(flow: FlowJson): string[] {
  const set = new Set<string>();
  for (const def of Object.values(flow.actions ?? {})) {
    if (!def?.pattern) continue;
    const p = def.pattern;
    if (!p.startsWith('tcp') && !p.startsWith('udp')) continue;
    const service = def.service?.trim();
    if (!service) continue;
    const proto = p.startsWith('tcp') ? 'tcp' : 'udp';
    set.add(`${proto}:${service}`);
  }
  return Array.from(set).sort();
}

/** 给定引用连接集合与已有 codec 文件名，返回缺失对应文件的连接名（排序）。纯函数，供单测。 */
export function findMissingCodecConnections(referenced: string[], codecFileNames: string[]): string[] {
  const have = new Set(codecFileNames);
  return referenced.filter((conn) => !have.has(connNameToCodecFileName(conn)));
}
```

**设计决策**：
- proto 推导用 `pattern.startsWith('tcp')?'tcp':'udp'`，与 `refsCheck.ts:370` 的逻辑一致（已存在的 proto 推导）。
- 命名换算规则与 `ResourcesDrawer.tsx:196-205` 的 `connNameToFileName`/`fileNameToConnName` **一致**（首个 `_`/`:` 互转 + `_codec.json` 后缀），但**独立实现**，未触碰 B1-B 的 4 个 UI 文件——满足 brief 的「最小改动为先、不改 ResourcesDrawer」约束。
- `collectFlowCodecConnections` 用 pattern 前缀判断，天然前瞻兼容 Batch-4 才加的 `tcpHeartbeat`/`udpHeartbeat`（只要 ActionPattern 类型扩展，前缀逻辑自动覆盖）。
- 排除规则：无 `pattern`、pattern 非 tcp/udp（httpRequest/setState/lua/clearState）、`service` 为空或纯空白 → 都不收集。

### 2.2 `services/index.ts` 导出

新增 4 个函数的 re-export（紧邻 `collectFlowScriptNames`/`syncFlowScriptsToIdb` 之后）：

```ts
export {
  collectFlowCodecConnections,
  connNameToCodecFileName,
  codecFileNameToConnName,
  findMissingCodecConnections,
} from './taskResourceDiff';
```

### 2.3 `taskActions.startTask` 接入 fail-loud 校验

在现有 `codecs.length === 0 → 「缺少协议配置」`之后、容量预检之前，加：

```ts
// §3.5：flow 引用的每条连接都必须在 IDB 有对应的 codec 文件，
// 否则 agent dial 时 CodecResolver 解析不到 codec 会 fail-loud。
// 这里前移到任务启动，给清晰中文提示（启动前拦截 vs 启动后失败）。
// 上传范围不变：下面仍发全部 codec 文件，agent resolver 加载全部。
{
  const codecFileNames = codecs.map((f) => f.name);
  const referenced = collectFlowCodecConnections(flowJson);
  const missing = findMissingCodecConnections(referenced, codecFileNames);
  if (missing.length > 0) {
    throw new ApiError(
      {
        code: 'INVALID_ARGUMENT',
        message:
          `以下连接缺少协议配置文件：${missing.join('，')}。` +
          `请在「协议配置」面板新建对应连接的协议配置。`,
        details: { missingCodecConnections: missing },
      },
      400,
    );
  }
}
```

**符合约束**：
- fail-loud（缺失即抛 `ApiError`，不 `??` 兜底、不静默）。
- 中文文案用「连接」「协议配置」，不暴露 codec/schema（变量名 `missingCodecConnections` 在 `details` 里保留技术名，UI 文案不暴露）。
- 上传范围不变：仍发全部 codec（`for (const f of codecs)` 那一段未动）。

## 3. 测试用例（`cmd/web/src/services/__tests__/flowCodecCoverage.test.ts`，12 个 it）

### `collectFlowCodecConnections`（3 例）
1. 扫描 tcp*/udp* 动作、去重、排序，排除无 service 与非 tcp/udp：构造 tcpConnect(logic)/tcpRequest(battle)/udpSend(battle)/udpRequest(battle 重复)/tcpListen(rank)/tcpClose(logic 重复)/setState/lua/httpRequest/tcpSend(无 service)/udpConnect(空白 service)，断言返回 `['tcp:battle','tcp:logic','tcp:rank','udp:battle']`。
2. `flow.actions` 缺失/为空对象 → `[]`。
3. 容忍动作缺 `pattern` 字段 → `[]`。

### `connNameToCodecFileName` / `codecFileNameToConnName`（4 例）
4. 连接名 → codec 文件名：`tcp:logic`/`udp:battle`。
5. codec 文件名 → 连接名：`tcp_logic_codec.json`/`udp_battle_codec.json`。
6. round-trip（含 `udp:battle`）。
7. `codecFileNameToConnName` 对无后缀名也不崩（`tcp_logic` → `tcp:logic`）。

### `findMissingCodecConnections`（5 例）
8. 引用连接都有对应文件 → `[]`。
9. 缺 `udp:battle` → `['udp:battle']`。
10. 结果保持引用顺序（空文件集 → 全部引用原序返回）。
11. 忽略 codec 文件名集合里的非 codec 文件（如 `errors.json`）。
12. 空引用 → 空结果。

## 4. 验证结果

### tsc
```
$ npx tsc -b
EXIT=0
```

### vitest
```
$ npm run test
Test Files  16 passed (16)
     Tests  189 passed (189)
  Duration  6.32s
```

新增的 `flowCodecCoverage.test.ts`：**12 passed**。总数从之前的 165 it 提升到 **189**（+24，含本任务 12 + 其它新测试文件），超过 brief 要求的 179+。

### 首次 tsc 失败的根因与修复
首次跑 tsc 报一堆 `TS1127 Invalid character`，定位到 JSDoc 注释里写了 `tcp*/udp*`——`*/` 序列会**提前结束 JSDoc 注释块**，后续中文被当成代码解析。修复：把 JSDoc 注释里的 `tcp*/udp*` 改写为 `tcp/udp`（`taskResourceDiff.ts` 的 `collectFlowCodecConnections` 注释 + 测试文件的文件头注释各一处）。字符串字面量里的 `tcp*/udp*`（如 `it('扫描 tcp*/udp* 动作…')`）不受影响，保留。

## 5. 自审（对照 brief 约束）

| 约束 | 遵守 |
|---|---|
| 改动限于 taskActions.ts + 放 helper 的 service 文件 + services/index.ts + 新测试 | ✅ 仅 4 个文件 |
| 严禁动 B1-B 的 4 UI 文件、resourcesStore/baselineApi | ✅ 未触碰 |
| 禁止兼容性兜底（缺失即 fail-loud 中文，不 `??`） | ✅ 直接抛 `ApiError` |
| UI 文案用「连接」「协议配置」不暴露 codec/schema | ✅ |
| proto 推导复用 refsCheck 的前缀思路 | ✅ |
| 命名换算与 ResourcesDrawer 一致但独立实现 | ✅ |
| 上传范围不变（仍发全部 codec） | ✅ 未动 `for (const f of codecs)` |
| `npx tsc -b` exit 0 | ✅ |
| `npm run test` 通过（≥179） | ✅ 189 |
| 不要 git commit | ✅ 未 commit |

## 6. 改动文件清单与 diff stat

本任务实际改动（worktree 分支上，未 commit）：

```
 cmd/web/src/services/index.ts            |  7 +++
 cmd/web/src/services/taskActions.ts      | 40 ++++++++++++----
 cmd/web/src/services/taskResourceDiff.ts | 82 ++++++++++++++++++++++++++++----
 3 files changed, 112 insertions(+), 17 deletions(-)

新增（untracked）：
 cmd/web/src/services/__tests__/flowCodecCoverage.test.ts  (12 tests)
```

> 注：`git diff --stat` 里还会列出 `ResourcesDrawer.tsx`、`resourcesStore.ts`、`baselineApi.ts`、`RuntimeBar.tsx` 等——那些是 **B1-A/B/C 前序任务**留在 worktree 分支上的既有改动，本任务未触及。本任务的改动严格限于上述 4 个文件。

## 7. 关联文件路径（绝对）

- 新 helper：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec\cmd\web\src\services\taskResourceDiff.ts`
- 校验接入：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec\cmd\web\src\services\taskActions.ts`
- 导出：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec\cmd\web\src\services\index.ts`
- 测试：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec\cmd\web\src\services\__tests__\flowCodecCoverage.test.ts`

## 8. Concerns

无。所有约束已满足，tsc/test 全绿。前序 B1 任务在 worktree 上的既有改动与本任务边界清晰，未交叉。
