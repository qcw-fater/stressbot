# T3 Batch-4 任务 B — routeKey 真实计算（§3.7）

> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）。
> 依赖：Batch 1 的多 codec 存储（`listCodecFiles`）+ `codecFileNameToConnName`；codec.json 有 `routeKeyTemplate`。

## 1. 任务定位

`refsGraph.ts:routeKey(route)` 现在是「JSON 排序伪 routeKey」（注释自陈「前端无法运行 Lua adapter，用排序 JSON
凑合覆盖 99% {cmd,act}」）。声明式 codec 后，routeKey 是**确定的**：`codec.json` 的 `routeKeyTemplate`
（如 `{cmd}:{act}`）+ route 字段值。本任务用真实 routeKeyTemplate 计算 routeKey，让 listen 去重/校验更准；
**协议配置缺失/有误时不静默 fallback 到伪 key**，而是提示先修复协议配置。

## 2. 现状（先读码）

**先读** `cmd/web/src/components/FlowEditor/listens/refsGraph.ts`（`routeKey(route)` 伪实现 ~84-96 + 用它的
`duplicateRegisters` 分组键 `${server}|${routeKey(route)}`）、`cmd/web/src/components/FlowEditor/validation/refsCheck.ts`
（用 routeKey 的处：listen 预注册校验 `${proto}:${service}|${routeKey(def.route)}` ~369-371；以及其它 routeKey
调用点——grep 全核）、refsCheck 的调用方（grep `refsCheck\|buildRefGraph`，看在哪触发、是否 async/可预加载）。
`cmd/web/src/services/resourcesStore.ts` 的 `listCodecFiles`、`cmd/web/src/services/taskResourceDiff.ts` 的
`codecFileNameToConnName`（B1-D，文件名→连接名）、`cmd/web/src/types/codec.ts`（CodecSchema.routeKeyTemplate）。
真实模板：`conf/adapter/tcp_logic_codec.json` 的 `routeKeyTemplate`（如 `{cmd}:{act}`）。

## 3. 实现规格

### 3.1 纯函数（新，可单测）

```ts
/** 按 routeKeyTemplate 把 route 字段值代入占位，返回真实 routeKey。占位字段缺失/非对象 → null。 */
export function computeRouteKey(template: string, route: unknown): string | null;
```
- 占位正则 `\{([A-Za-z_]\w*)\}`（与 validateCodecSchema 的 routeKey 占位一致）。
- route 非对象 → null。占位字段在 route 中缺失 → null（不可解析）。
- 例：`computeRouteKey('{cmd}:{act}', {cmd:1, act:2})` → `'1:2'`；`computeRouteKey('{cmd}:{act}', {cmd:1})` → null。
- 单测：正常代入、多占位、缺失字段→null、非对象 route→null、无占位模板原样返回、数值/字符串值。

### 3.2 codec 模板加载（async，IDB）

```ts
/** 加载所有 codec 的 routeKeyTemplate：server('tcp:logic') → template。 */
export async function loadRouteKeyTemplates(): Promise<Map<string, string>>;
```
- `listCodecFiles()` → 每份 `JSON.parse(content)` → 取 `routeKeyTemplate`（字符串）+ 由文件名经
  `codecFileNameToConnName` 得 server。解析失败/无 template 的条目跳过（不抛）。
- 返回 Map。空 Map = 无可用 codec（调用方据此决定降级策略）。
- 单测：mock listCodecFiles，断言 Map 内容 + 跳过坏文件。

### 3.3 接入 refsGraph / refsCheck

- 把「伪 routeKey」替换为「**server 感知**的真实 routeKey」：当该 server 在 templates Map 中有 template 时，
  用 `computeRouteKey(template, route)`；**否则**用旧伪 routeKey 作降级，**但**对该情况**显式产出一条 warning**
  （新 issue code，如 `ROUTEKEY_CODEC_MISSING`），文案如「连接 X 的协议配置缺失或有误，listen 去重使用近似匹配，请先在协议配置面板修复」——**不静默伪 key**。
- 设计选择（你定，但须自洽）：把 templates Map 经参数透传进 buildRefGraph/refsCheck（调用方先 `await loadRouteKeyTemplates()` 再跑校验），或 module-level cache + 失效。**优先透传**（避免缓存失效复杂性）；若调用方全 sync 不易改 async，再用 cache 并注释失效条件。读 refsCheck 调用方决定。
- routeKey 不可解析（computeRouteKey 返回 null，即 route 缺占位字段）时，沿用伪 routeKey（这种情况是 flow 数据问题，不是 codec 问题，伪 key 仍能查重）。

### 3.4 不破坏既有 listen 校验

duplicateRegisters（listen 去重）+ listen 预注册校验语义不变，只是 routeKey 从「总伪」变「有 codec 时真、无 codec 时伪+warning」。既有 refsCheck 测试不应回归（有 codec 时结果更准；无 codec 时行为同前 + 多一条 warning）。

## 4. 全局约束（bind）

- **改动文件**：`refsGraph.ts`（routeKey server 感知 + warning）、`refsCheck.ts`（透传 templates + ROUTEKEY_CODEC_MISSING warning）、新纯函数/加载 helper（放 refsGraph.ts 或新 `routeKeyResolver.ts`）+ 测试、refsCheck 调用方（透传 templates，若选透传方案）。**严禁动** services/（只用 listCodecFiles/codecFileNameToConnName）、codecEditor/、types/、后端。
- **禁止静默伪 key**：codec 缺失/有误 → 显式 warning，不悄悄用伪 routeKey 当没事。
- **禁止兼容兜底**：computeRouteKey 解析不出（route 缺字段）→ 伪 key 降级（这是数据问题，合理），但不 `??` 兜错误。
- 类型安全；UI 文案中文、不暴露 codec/schema 术语（用「协议配置」「连接」）；**不要 git commit**。
- 按 TDD：computeRouteKey + loadRouteKeyTemplates 先写测试。

## 5. 验证（全绿才算 DONE）

- `cd cmd/web && npx tsc -b` exit 0。
- `cd cmd/web && npm run test` 通过（computeRouteKey/loadRouteKeyTemplates 新单测 + 既有 refsCheck 不回归）。
- 自查 `git diff --stat`：改动限于上述文件。

## 6. 报告

写到 `plans/declarative-codec/reports/t3-b4b-routekey-real-report.md`：实现要点、computeRouteKey/loadRouteKeyTemplates、
接入方案（透传 vs cache，选哪个+理由）、warning 机制（不静默伪 key）、既有校验不回归说明、测试、tsc/test 结果、自审、贴 `git diff --stat`。

返回消息只含：① 状态；② 改动文件清单；③ 一行测试摘要；④ concerns（尤其透传 vs cache 的选择 + 调用方改动范围）。有歧义先问。
