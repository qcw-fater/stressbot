# B2-3 + B2-4 报告：预览强化 + 表单密度

## 任务范围

- **B2-3 预览强化**：ByteStrip 标尺刻度加密、PreviewPanel hex 分组显示、ProtocolConfigEditor 实时校验 Alert ul padding 修正。
- **B2-4 表单密度**：PipelineEditor 单步卡片字段分组（3 组 + Divider）、Field helper 横排。

只动展示层细节，零业务逻辑改动（预览请求 / hex 解析 / 管线增删改 state 与 handler 全部不变）。

---

## 1. ByteStrip 标尺刻度加密（B2-3.1）

**原状**：偏移标尺只标 3 个 tick `[0, floor(headerSize/2), headerSize-1]`，headerSize 较大时刻度稀疏，难精确定位字节偏移。

**方案**：自适应间隔——按 headerSize 分档，每 N 字节一个 tick，N 越大刻度越疏：

```ts
function tickStepFor(headerSize: number): number {
  if (headerSize <= 8) return 1;   // 全程密集
  if (headerSize <= 16) return 2;
  if (headerSize <= 32) return 4;
  return 8;                         // 大 header 避免拥挤
}

function rulerTicks(headerSize: number): number[] {
  if (headerSize <= 0) return [];
  const step = tickStepFor(headerSize);
  const ticks: number[] = [];
  for (let i = 0; i < headerSize; i += step) ticks.push(i);
  // 末字节单独补（若未自然落在 step 倍数上），便于看末边界
  const last = headerSize - 1;
  if (ticks[ticks.length - 1] !== last) ticks.push(last);
  return ticks;
}
```

**效果**：
- headerSize=4 → ticks: `[0,1,2,3]`（4 个，全程精确）
- headerSize=8 → ticks: `[0,1,...,7]`（8 个）
- headerSize=16 → ticks: `[0,2,4,6,8,10,12,14,15]`（9 个，末尾补 15）
- headerSize=32 → ticks: `[0,4,8,...,28,31]`（9 个 + 末尾 31）
- headerSize=64 → ticks: `[0,8,16,...,56,63]`（9 个 + 末尾 63）

tick span 绝对定位 `left: tick * BYTE_PX`，与字节块位置一致（均按 `BYTE_PX=18` 计算），保持视觉对齐。

**位置**：`ByteStrip.tsx:24-58`（新增 helper）+ `:163-171`（替换原 3-tick 渲染）。

---

## 2. PreviewPanel hex 分组 + 复制按钮（B2-3.2）

### 2.1 hex 按字节分组

**原状**：HexOutput 直接展示原始 hex 字符串，长 hex 连成一片难读。

**方案**：新增 `formatHexGrouped` helper，仅用于展示（输入回灌仍走原字符串，HexInput 不变）：

```ts
function formatHexGrouped(raw: string): string {
  const cleaned = raw.replace(/[^0-9a-fA-F]/g, '');
  if (cleaned.length === 0) return '';
  const bytes: string[] = [];
  for (let i = 0; i < cleaned.length; i += 2) {
    bytes.push(cleaned.slice(i, i + 2));
  }
  const lines: string[] = [];
  for (let i = 0; i < bytes.length; i += 16) {
    const lineBytes = bytes.slice(i, i + 16);
    // 每 8 字节插一个额外空格作为「半行」分隔
    const parts: string[] = [];
    for (let j = 0; j < lineBytes.length; j += 8) {
      parts.push(lineBytes.slice(j, j + 8).join(' '));
    }
    lines.push(parts.join('  '));
  }
  return lines.join('\n');
}
```

**格式规则**：
- 抽取所有 hex 字符，每两位一字节
- 字节间空格分隔；每 8 字节加双空格半行分隔；每 16 字节换行
- 示例：`0a2b3c4d5e6f708090a1b2c3d4e5f607` →
  ```
  0a 2b 3c 4d 5e 6f 70 80  90 a1 b2 c3 d4 e5 f6 07
  ```

HexOutput 容器样式调整为 `whiteSpace: 'pre-wrap'`（保留空格和换行）+ `lineHeight: 1.6`（多行可读）。字体维持 `monospace`（tokens.css 无等宽变量，用关键字 monospace 是浏览器原生约定，非硬编码色值）。

### 2.2 复制按钮

**已存在**：实读 PreviewPanel.tsx 后发现 HexOutput 早有复制按钮（:85-97），已用：
- antd `<Button size="small" type="text" icon={<CopyOutlined />} />`
- `navigator.clipboard?.writeText(val)`
- `AntApp.useApp().message.success('已复制')` / `message.error('复制失败')`

**零改动**——合规检查通过，无需重做。

**位置**：`PreviewPanel.tsx:75-149`（HexOutput + 新增 formatHexGrouped）。

---

## 3. ProtocolConfigEditor Alert ul padding（B2-3.3）

**原状**：实时校验 Alert 内 `<ul style={{ margin: 0, paddingLeft: 18, ... }}>` —— paddingLeft:18 与 Alert description 默认 padding 叠加偏移不规范。

**改动**：`paddingLeft: 18 → 20`（标准列表缩进）。margin/overflow/maxHeight 保留。

```diff
- <ul style={{ margin: 0, paddingLeft: 18, maxHeight: 120, overflow: 'auto' }}>
+ <ul style={{ margin: 0, paddingLeft: 20, maxHeight: 120, overflow: 'auto' }}>
```

**位置**：`ProtocolConfigEditor.tsx:473`（唯一一处改动）。

---

## 4. PipelineEditor 字段分组（B2-4.1）

### 4.1 当前字段顺序与分组归类

实读 PipelineStepCard，按语义归为 3 组：

| 组 | 含字段 / 子表单 | 说明 |
|----|------------------|------|
| **组1：基本属性** | name / op / 算法 / onError / flag + 移序/删除按钮 | 行 1 的 Space wrap + opInvalid 即时提示 |
| **组2：输入处理** | encrypt 专属（keyLen / offset.encode / offset.decode）+ OverSubform（checksum/hash 独立）+ ParamsDynamic（算法动态参数） | op 决定子集 |
| **组3：输出与条件** | ProducesSubform + WhenSubform | 永远渲染 |

### 4.2 Divider 用法

antd `Divider` 加 `dashed` + `style={{ margin: '8px 0' }}`（spec 指定）：

```tsx
{/* ── 组1：基本属性 ── */}
<Space size={8} wrap align="center"> ... name/op/算法/onError/flag + 移序/删除 ... </Space>
{opInvalid && <Typography.Text type="danger">...</Typography.Text>}

{/* ── 组2：输入处理 ── */}
{(isEncrypt || isStandaloneDigest || selectedAlgo) && (
  <Divider style={{ margin: '8px 0' }} dashed />
)}
{isEncrypt && ( ... keyLen/offset ... )}
{isStandaloneDigest && <OverSubform ... />}
<ParamsDynamic ... />

{/* ── 组3：输出与条件 ── */}
<Divider style={{ margin: '8px 0' }} dashed />
<ProducesSubform ... />
<WhenSubform ... />
```

**条件渲染决策**：
- 组1→组2 Divider：条件渲染（仅当 `isEncrypt || isStandaloneDigest || selectedAlgo` 时显示，避免组2 区无内容时出现孤儿 Divider）。极端边界：selectedAlgo 存在但其 params 元数据为空（ParamsDynamic 返回 null）时 Divider 会单独出现，可接受（属加载失败的暂态）。
- 组2→组3 Divider：**总是渲染**（组3 永远有 produces/when，需要分隔）。

**位置**：`PipelineEditor.tsx:220-378`（重排卡片内字段 + 两条 Divider）。

---

## 5. Field helper 横排（B2-4.2）

**原状**：Field helper label 独占一行（`display: 'block'` + marginBottom:2），label 与控件纵向堆叠，占用纵向空间。

**方案**：label + 控件同一行（flex 横排），窄时自动 wrap：

```tsx
function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'center',
        flexWrap: 'wrap',     // 窄时回退 label 独占行
        minWidth: 0,
      }}
    >
      <Typography.Text
        type="secondary"
        style={{
          fontSize: 12,
          flex: '0 0 80px',    // label 固定宽 80px，与控件对齐
          marginRight: 6,
          whiteSpace: 'nowrap',
        }}
      >
        {label}
      </Typography.Text>
      <div style={{ flex: '1 1 auto', minWidth: 0 }}>{children}</div>
    </div>
  );
}
```

**决策**：
- label `flex: '0 0 80px'` 固定 80px（与 spec 推荐一致；现有 label 最长「offset.decode（收）」约 13 中文字 ≈ 100px+，但实际 UI 中 80px 会超长换行，此时 flexWrap 让 label 占满后控件下行）。
- 控件包一层 `<div style={{ flex: '1 1 auto', minWidth: 0 }}>` 以正确响应 Space wrap 内的宽度收缩。
- `alignItems: center` —— spec 明确要求。
- 由于 Field 在 Space wrap 内使用，Space 本身负责横向排布，Field 自身只在「label + 单个控件」间做横排。

**位置**：`PipelineEditor.tsx:784-811`（Field helper 重写）。

---

## 6. 验证结果

### 6.1 tsc

```
$ cd cmd/web && npx tsc -b
（exit 0，无输出）
```

### 6.2 Vitest

```
Test Files  22 passed (22)
     Tests  287 passed (287)
```

全部通过，无回归。

### 6.3 git diff --stat（B2-3+4 改动的 5 个目标文件）

```
cmd/web/src/components/modules/ProtocolConfigEditor.tsx         | 仅 paddingLeft:18→20（其余为 B2-1+2 残留）
cmd/web/src/components/modules/codecEditor/ByteStrip.tsx        | 58 ++--  （标尺加密 + helper）
cmd/web/src/components/modules/codecEditor/PipelineEditor.tsx   | 40 ++--  （Divider 分组 + Field 横排）
cmd/web/src/components/modules/codecEditor/PreviewPanel.tsx     | 34 ++--  （formatHexGrouped + 白空格样式）
cmd/web/src/components/modules/codecEditor/codecEditor.css      | （B2-1+2 残留，本任务零改动）
```

**注意**：工作树中 `ProtocolConfigEditor.tsx` 和 `codecEditor.css` 的 diff 大部分来自上一批次 B2-1+2（布局重构 + 配色统一）未提交的工作；本任务仅 `ProtocolConfigEditor.tsx` 的 `paddingLeft: 18→20` 一行属于 B2-3。其他 `conf/` `docs/` `HeaderFieldTable.tsx` 的改动与本任务无关（listen/ranked 迁移的进行中工作）。

### 6.4 硬编码色 grep 证据

```
$ git grep -n "#[0-9a-fA-F]\{6\}" \
    cmd/web/src/components/modules/codecEditor/ByteStrip.tsx \
    cmd/web/src/components/modules/codecEditor/PreviewPanel.tsx \
    cmd/web/src/components/modules/codecEditor/PipelineEditor.tsx
（exit 1，无输出——零硬编码色）
```

三个文件均无任何硬编码 `#xxxxxx` 色值（FIELD_COLORS 在 `byteLayout.ts`，不在 grep 范围内）。

---

## 7. 自审

- [x] 颜色全部走 tokens.css 变量（ByteStrip 早已用 `var(--text-tertiary)` / `var(--divider-bg)` / `var(--color-error)`；PreviewPanel/PipelineEditor 新增代码无任何颜色属性）。
- [x] 暗色主题自动跟随（所有色值均 token 变量）。
- [x] antd v5：Divider dashed + style 合规；Button 用法未改；Field helper 内部用原生 div + Typography.Text，无废弃 API。
- [x] 复制功能本就合规（`navigator.clipboard` + antd message，禁 alert）。
- [x] 业务逻辑零改动：runPreview / HexInput.onChange / patch / updatePipelineStep / movePipelineStep / removePipelineStep 全部未动。
- [x] UI 文本中文（复制按钮本就「复制」/「已复制」，本任务新增代码无新 UI 文本）。
- [x] 287 测试全过，无回归。

---

## 8. 设计决策（UI 看不到，供验证）

1. **ByteStrip 刻度阈值**：`≤8→1 / ≤16→2 / ≤32→4 / >32→8`。理由：BYTE_PX=18，宽度阈值约对应 144/288/576/1152px，刻度密度从 18px/格到 144px/格，保证「最密不挤、最疏不丢末尾」。headerSize=64 时仅 9 个 tick + 末尾 1 个，视觉清爽。
2. **formatHexGrouped 选 16 字节/行**：与业界 hex viewer 惯例一致（xxd / HxD 默认 16 字节），每 8 字节半行分隔提升长 hex 可读性。
3. **PipelineEditor 组2 Divider 条件渲染**：仅当 encrypt/checksum/hash/selectedAlgo 任一为真时显示组1→组2 Divider；边界情况 selectedAlgo 存在但 params 空时 Divider 孤儿，可接受（加载失败暂态，正常用不出）。
4. **Field helper label 固定 80px**：spec 推荐 80px。中文长 label（如「offset.decode（收）」约 100px）会换行，flexWrap 回退为 label 占满 + 控件下行，保证可读不裁切。
5. **HexOutput 字体 monospace**：tokens.css 无等宽变量，用 CSS 关键字 `monospace`（浏览器原生，非硬编码色值）。
