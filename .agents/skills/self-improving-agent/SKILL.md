---
name: self-improving-agent
description: Use when 发生命令失败、工具异常、用户纠正、知识缺口、外部 API 行为与预期不符、重复问题模式，或执行重大任务前需要回顾 .learnings 经验时。
metadata:
  version: "2.0.0"
  based-on: "peterskoett/self-improvement v1.0.1"
---

# 自我改进技能

## 与 memory/ 系统的分工

| 存储位置 | 存什么 | 何时写 |
|---------|-------|-------|
| `memory/` | 用户偏好、项目决策、架构约束、长期事实 | AI 从对话中学到的跨项目通用知识 |
| `.learnings/` | 命令错误、工具失败、可重现的技术坑 | 发生具体技术错误/问题时 |
| `AGENTS.md` | 项目规范、编码约束、必须遵循的规则 | 经验被验证为普遍适用后固化 |

> **原则**：用户偏好 → memory；技术错误 → .learnings；普遍规则 → AGENTS.md。不重复记录。

---

## 初始化（仅需执行一次）

```bash
mkdir -p .learnings && \
[ -f .learnings/LEARNINGS.md ] || printf "# 经验教训\n\n分类: correction | insight | knowledge_gap | best_practice\n\n---\n" > .learnings/LEARNINGS.md && \
[ -f .learnings/ERRORS.md ] || printf "# 错误记录\n\n命令执行失败、工具异常、集成错误\n\n---\n" > .learnings/ERRORS.md
```

⚠️ **严禁记录**：密钥、令牌、环境变量、完整配置文件、原始命令输出。

---

## 自动触发条件

| 信号 | 记录到 | 优先级 |
|-----|-------|-------|
| 命令返回非零退出码 | ERRORS.md | high |
| 出现异常堆栈/超时 | ERRORS.md | high |
| 用户说"不对"、"你搞错了"、"其实应该" | LEARNINGS.md (correction) | medium |
| 用户提供 AI 不知道的技术事实 | LEARNINGS.md (knowledge_gap) | medium |
| API/工具行为与文档不符 | ERRORS.md | medium |
| 发现更优解法 | LEARNINGS.md (best_practice) | low |

**不触发**：用户偏好、项目决策、架构选择 → 直接写 memory/。

---

## 日志模板

### 错误记录（追加到 ERRORS.md）

```markdown
## [ERR-YYYYMMDD-XXX] 工具/命令名称

**时间**: YYYY-MM-DDTHH:MM:SSZ
**优先级**: high | medium
**状态**: pending
**领域**: backend | infra | config | tests

### 摘要
一句话：做了什么、失败了

### 错误信息
```
脱敏后的错误消息（不超过20行）
```

### 上下文
- 操作：
- 环境：Windows 10, Go 版本等
- 复现步骤：

### 修复方案
具体可执行的解决步骤（禁止写"需要调查"）

### 元数据
- 可复现: yes | no
- 关联文件: path/to/file
- 关联条目: ERR-YYYYMMDD-XXX（如有）

---
```

### 经验教训（追加到 LEARNINGS.md）

```markdown
## [LRN-YYYYMMDD-XXX] correction | insight | knowledge_gap | best_practice

**时间**: YYYY-MM-DDTHH:MM:SSZ
**优先级**: low | medium | high
**状态**: pending
**领域**: backend | infra | config | tests

### 摘要
一句话核心收获

### 详情
发生了什么 → 哪里错了 → 正确做法是什么

### 建议行动
具体可执行步骤

### 元数据
- 来源: conversation | error | user_feedback
- 关联文件: path/to/file
- 标签: tag1, tag2
- 复发次数: 1（如果是重复问题填写）

---
```

---

## 条目 ID 规则

`类型-年月日-序号`：`ERR-20260422-001`、`LRN-20260422-002`

---

## 状态流转

| 状态 | 含义 |
|-----|-----|
| pending | 待处理 |
| resolved | 已解决（添加解决信息块） |
| promoted | 已固化到 AGENTS.md |
| wont_fix | 不处理（备注原因） |

解决后追加：
```markdown
### 解决信息
- **时间**: YYYY-MM-DDTHH:MM:SSZ
- **方法**: 简要说明
```

---

## 固化规则

满足以下**全部条件**时，将经验固化到 AGENTS.md 并将状态改为 `promoted`：
- 复发次数 ≥ 2
- 适用于所有类似任务（不只是当前场景）
- 是技术规范而非用户偏好

固化格式：一行规则，不加背景故事。

---

## 重复模式处理

记录前先检索：
```bash
grep -r "关键词" .learnings/
```

同一问题出现 ≥ 2 次 → 优先级升为 `high`，添加 `复发次数` 字段。

---

## 定期审查

以下时机必须查看 `.learnings/` 待处理条目：
- 开始新功能开发前
- 进入有历史问题的模块前

```bash
# 查看所有待处理条目
grep -l "pending" .learnings/*.md

# 查看高优先级条目
grep -B3 "优先级.*high" .learnings/ERRORS.md .learnings/LEARNINGS.md
```

---

**版本**: 2.0.0 | **更新**: 2026-04-22
