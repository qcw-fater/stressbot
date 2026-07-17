---
name: git-management
description: Use when 在 stressbot 项目内需要查看变更、提交代码、拉取更新、解决冲突、查看历史记录、撤销修改、创建分支、推送、打 tag 或发布等 Git 操作时。
---

# Git 版本控制管理技能

## 项目 Git 基本信息

- **远程仓库**: `https://gitee.com/qcw-fater/stressbot.git`
- **主分支**: `master`
- **工作副本路径**: `D:\Gitee\stressbot`

---

## 最高优先级禁令（必须遵守）

以下规则优先级高于后续所有常规命令示例：

1. **严禁自行创建分支**
   - 不得在用户未明确要求或确认时执行 `git switch -c` / `git checkout -b` / `git branch <name>` 等创建分支操作。
   - 即使当前位于 `master` / 默认分支，也不能擅自创建所谓“安全分支”。
   - 如提交/推送流程需要分支选择，必须先向用户确认目标分支或由用户明确授权。

2. **严禁自主提交被 `.gitignore` 忽略的文件**
   - 不得自行使用 `git add -f` 强制纳入被忽略文件。
   - 特别注意：本项目 `deploy/` 目录被 `.gitignore` 忽略。用户要求修改或执行 `deploy/upgrade.sql` 仅代表本地升级用途，不等于允许提交该文件。
   - 若确实需要提交被忽略文件，必须先明确告知该文件被忽略，并取得用户对具体文件的确认。

3. **非常规 / 破坏性 / 远程操作必须确认**
   - 删除远程分支、强推、reset、rebase、切换分支、创建分支、提交 ignored 文件、修改远程配置等操作，必须先获得用户明确确认。
   - 不允许“自己独立思考后”替用户决定这些 Git 流程。
   - 用户只说“commit / push”时，只能按当前分支和当前可追踪文件进行标准提交流程；如遇默认分支、ignored 文件、未追踪文件、分支策略不明确等情况，应先确认。

4. **本项目 Git 操作必须使用本技能**
   - 在 `D:\Gitee\stressbot` 项目内进行任何 Git 操作前，必须先遵循本技能流程。
   - 不得绕开本技能直接按通用 Git 习惯操作。

## 一、状态检查

### 查看工作区状态
```bash
git status
```

**状态符号说明**：

| 符号/区域 | 含义 |
|-----------|------|
| `Changes to be committed`（绿色） | 已暂存（staged），将随下次 commit 提交 |
| `Changes not staged for commit`（红色） | 已修改但未暂存 |
| `Untracked files`（红色） | 未纳入版本控制 |
| `M`  | 已修改（Modified） |
| `A`  | 已标记添加（Added） |
| `D`  | 已标记删除（Deleted） |
| `??` | 未纳入版本控制（Untracked） |
| `!!` | 已被 .gitignore 忽略 |

### 查看当前仓库信息
```bash
git remote -v
git branch -a
```

### 查看指定文件差异
```bash
# 工作区 vs 暂存区（未暂存的修改）
git diff <文件路径>

# 暂存区 vs 最近提交（已暂存的修改）
git diff --cached <文件路径>
```

### 查看所有未提交变更的差异
```bash
# 包含已暂存和未暂存的所有修改
git diff HEAD
```

---

## 二、拉取与更新

### 拉取远程更新并合并
```bash
git pull
```

### 仅拉取不合并（查看远程变更）
```bash
git fetch
git log HEAD..origin/master --oneline
```

### 拉取并变基（保持线性历史）
```bash
git pull --rebase
```

---

## 三、提交操作

### 提交规范

提交信息应简洁明确，说明 **为什么** 做这个改动：

```
<类型>: <简要描述>

<详细说明（可选）>
```

**类型参考**：

| 类型 | 用途 |
|------|------|
| `feat` | 新功能 |
| `fix` | 缺陷修复 |
| `refactor` | 重构（不改变行为） |
| `perf` | 性能优化 |
| `config` | 配置变更 |
| `docs` | 文档 |
| `chore` | 构建/工具/杂项 |

**示例**：
```
feat: 添加 nested/nestedList binding 类型支持

支持声明式嵌套 proto 消息赋值，消除 gm_add_hero 等 Lua 脚本
```

### 标准提交流程

```bash
# 第一步：查看变更范围
git status

# 第二步：查看具体变更内容
git diff

# 第三步：暂存指定文件（推荐，精确控制范围）
git add <文件路径> [<文件路径2> ...]

# 第四步：确认暂存内容
git diff --cached

# 第五步：提交
git commit -m "提交信息"
```

### 暂存文件
```bash
# 暂存指定文件（推荐）
git add conf/flow/flow.json engine/action.go

# 暂存所有已修改和已删除的文件（不含未追踪文件）
git add -u

# 暂存所有变更（含未追踪文件，谨慎使用）
git add -A
```

### 修改最近一次提交（尚未 push 时）
```bash
# 追加暂存的修改到上一次提交，可同时修改提交信息
git commit --amend -m "新的提交信息"
```

---

## 四、推送操作

### 远程仓库配置

本项目配置了两个远程仓库：

| 别名 | 地址 | 用途 |
|------|------|------|
| `origin` | `https://gitee.com/qcw-fater/stressbot.git` | 主仓库（日常开发） |
| `github` | `https://github.com/qcw-fater/stressbot.git` | CI/CD（自动构建发布） |

```bash
# 查看当前远程配置
git remote -v
```

### 推送到远程
```bash
# 首次推送新分支（需推两个仓库）
git push -u origin <分支名>
git push -u github <分支名>

# 日常推送（分别推送）
git push origin master    # → Gitee
git push github master    # → GitHub
```

### 强制推送（仅限自己的分支，禁止对 master 使用）
```bash
git push --force-with-lease
```

---

## 五、分支操作

### 查看分支
```bash
# 本地分支
git branch

# 所有分支（含远程）
git branch -a
```

### 创建与切换分支
```bash
# 创建并切换到新分支
git checkout -b <新分支名>

# 切换到已有分支
git checkout <分支名>
```

### 合并分支
```bash
# 先切到目标分支
git checkout master

# 合并其他分支
git merge <分支名>
```

### 删除分支
```bash
# 删除已合并的本地分支
git branch -d <分支名>

# 强制删除本地分支
git branch -D <分支名>

# 删除远程分支
git push origin --delete <分支名>
```

---

## 六、撤销操作

### 撤销工作区修改（未暂存）
```bash
# 撤销指定文件
git checkout -- <文件路径>

# 撤销所有未暂存的修改
git checkout -- .
```

### 取消暂存（回到已修改未暂存状态）
```bash
git reset HEAD <文件路径>
```

### 撤销最近一次提交（保留修改在工作区）
```bash
git reset HEAD~1
```

### 撤销最近一次提交（保留修改在暂存区）
```bash
git reset --soft HEAD~1
```

### 撤销最近一次提交（丢弃所有修改，慎用）
```bash
git reset --hard HEAD~1
```

---

## 七、历史记录

### 查看提交历史
```bash
# 最近 10 条
git log --oneline -10

# 指定文件的提交历史
git log --oneline -20 <文件路径>

# 图形化分支历史
git log --oneline --graph --all -20

# 详细模式（显示变更文件列表）
git log --stat -5
```

### 查看指定提交的变更详情
```bash
# 查看改动了哪些文件
git show --stat <提交哈希>

# 查看完整差异
git show <提交哈希>

# 查看指定提交中某文件的变更
git show <提交哈希> -- <文件路径>
```

### 查看指定版本的文件内容
```bash
git show <提交哈希>:<文件路径>
```

### 比较两个提交
```bash
git diff <哈希1>..<哈希2>
git diff <哈希1>..<哈希2> -- <文件路径>
```

---

## 八、冲突解决

### 发现冲突
```bash
git status | grep "both modified"
```

### 冲突解决流程

1. **手动编辑冲突文件**，解决 `<<<<<<`、`=======`、`>>>>>>` 标记的冲突
2. **标记已解决**：
   ```bash
   git add <冲突文件路径>
   ```
3. **完成合并**：
   ```bash
   git commit -m "解决冲突：<说明>"
   ```

### 冲突时放弃本地修改（使用远程版本）
```bash
git checkout --theirs <冲突文件>
git add <冲突文件>
```

### 放弃本地修改（使用本地版本）
```bash
git checkout --ours <冲突文件>
git add <冲突文件>
```

### 放弃整个合并
```bash
git merge --abort
```

---

## 九、暂存工作区（stash）

### 临时保存当前修改
```bash
git stash
```

### 恢复暂存的修改
```bash
git stash pop
```

### 查看暂存列表
```bash
git stash list
```

---

## 十、提交前检查清单

在执行 `git commit` 之前，确认以下事项：

- [ ] `git status` 确认暂存范围符合预期，无多余文件
- [ ] `git diff --cached` 检查变更内容，无调试代码（`fmt.Println`、临时注释等）
- [ ] **审查全部变更归属**：`git diff --stat` 中的所有变更可能包含其他协作者的改动，不能只关注自己修改的文件。审查每个文件的 diff 内容，理解变更意图和来源，按逻辑分组拆分 commit。不要 `git add -A` 盲目全包，也不要只 add 自己改的文件而忽略别人的。
- [ ] 新增文件已执行 `git add`
- [ ] 提交信息简洁明确，说明改动原因
- [ ] 代码通过本地编译：`go build ./...`
- [ ] flow.json 变更已在前端编辑器校验报告中确认无错误

---

## 十一、常用组合操作

### 查看今天的所有提交
```bash
git log --since="today" --oneline
```

### 查看某文件的修改历史（谁改了什么）
```bash
git log -p -- <文件路径>
```

### 查看某行代码的修改责任人
```bash
git blame <文件路径>
```

### 仅提交已追踪的修改文件（排除未追踪文件）
```bash
git add -u
git commit -m "提交信息"
```

---

## 十二、Tag 管理

### 版本号规范（语义化版本）

```
v<主版本>.<次版本>.<修订号>

v1.0.0  v1.1.0  v1.1.1  v2.0.0
```

| 递增时机 | 示例 | 含义 |
|----------|------|------|
| 主版本 | v1.x → v2.0 | 不兼容的 API 变更 |
| 次版本 | v1.0 → v1.1 | 向后兼容的新功能 |
| 修订号 | v1.1.0 → v1.1.1 | 向后兼容的 bug 修复 |

### 查看 tag

```bash
# 列出所有 tag
git tag

# 按模式筛选
git tag -l "v1.*"

# 查看 tag 对应的提交
git show v1.0.0 --stat
```

### 创建 tag

```bash
# 轻量 tag（仅标记提交，不附信息）
git tag v1.0.0

# 附注 tag（推荐，含作者、日期、说明）
git tag -a v1.0.0 -m "feat: 首个正式发布版本"
```

### 推送 tag

```bash
# 推送单个 tag
git push origin v1.0.0

# 推送所有本地 tag
git push origin --tags
```

### 删除 tag

```bash
# 删除本地 tag
git tag -d v1.0.0

# 删除远程 tag
git push origin --delete v1.0.0
```

### 基于 tag 创建分支（热修复）

```bash
git checkout -b hotfix/v1.0.1 v1.0.0
# 修复完成后合并回 master，然后打新 tag v1.0.1
```

---

## 十三、发布流程

项目已配置 GitHub Actions 自动发布流水线（`.github/workflows/release.yml`）。推送 `v*` 格式的 tag 会自动触发构建和发布。

### 发布前检查

- [ ] 所有代码已合并到 `master` 并推送
- [ ] `go build ./...` 编译通过
- [ ] `cd cmd/web && npx tsc --noEmit` 前端类型检查通过
- [ ] 功能验证完成（按 AGENTS.md 验证流程）
- [ ] 工作区干净，无未提交变更

### 有未提交变更时的处理

```bash
# 先确认有哪些变更
git status
git diff

# 情况 A：变更属于本次发布内容
#   → 提交后一起发布
git add <相关文件>
git commit -m "feat: 本次发布包含的改动"
git push

# 情况 B：变更是半成品，不属于本次发布
#   → 暂存到 stash，发布后恢复
git stash
# 发布完成后...
git stash pop

# 情况 C：确认发布后检查
#   确保 tag 指向的提交包含所有期望的变更
git log --oneline -10   # 确认最新提交
git diff v1.0.0..HEAD   # 确认 tag 后无遗漏
```

### 发布步骤

```bash
# 1. 确认当前在 master 且是最新的
git checkout master
git pull

# 2. 确保代码已推送到两个仓库
git push origin master     # → Gitee
git push github master     # → GitHub

# 3. 创建附注 tag
git tag -a v1.0.0 -m "feat: 首个正式发布版本"

# 4. 推送 tag 到 GitHub（触发 CI/CD 自动构建发布）
git push github v1.0.0
```

> **注意**：tag 只需推送到 GitHub（`github`），不需要推到 Gitee。CI/CD 流水线只在 GitHub Actions 上运行。

推送后等待 GitHub Actions 完成（约 3~5 分钟），Release 页面会自动出现下载包。

### CI/CD 自动产出

| 文件名 | 内容 |
|--------|------|
| `stressbot-vX.Y.Z-linux-amd64.tar.gz` | `agent` + `admin` + `conf/` + `dist/` + `nginx.conf` |
| `stressbot-vX.Y.Z-windows-amd64.zip` | `agent.exe` + `admin.exe` + `conf/` + `dist/` + `nginx.conf` |

### 查看 CI 构建状态

```bash
# 命令行查看
gh run list --limit 5
gh run view

# 或直接访问 GitHub 仓库的 Actions 页面
```

### 撤回有问题的发布

```bash
# 1. 删除 GitHub 上的远程 tag（会停止正在运行的 CI）
git push github --delete v1.0.0

# 2. 删除本地 tag
git tag -d v1.0.0

# 3. 在 GitHub Releases 页面手动删除对应 Release

# 4. 修复问题后重新打 tag
git tag -a v1.0.1 -m "fix: 修复 xxx 问题"
git push github v1.0.1
```

---

## 十四、注意事项

1. **禁止提交编译产物**：`*.exe`、`*.o`、`bin/`、`stressbot.exe` 等不应纳入版本控制
2. **禁止提交配置敏感文件**：含密码、密钥的本地配置文件
3. **提交前先拉取**：`git pull` → 解决冲突 → `git commit` → `git push`
4. **`??` 状态文件逐一确认**：新建文件需要 `git add`，不需要的文件不要误添加
5. **`.Codex/` 目录无需提交**：该目录为 Codex 工具本地配置，已在 `.gitignore` 中排除
6. **`log/` 目录无需提交**：运行日志目录，已在 `.gitignore` 中排除
7. **禁止对 master 强制推送**：`git push --force` 可能覆盖他人提交
8. **禁止自动`git commit`和`git push`**：`git commit`和`git push` 前一定要展示待提交/推送的全部文件列表供用户确认
9. **提交信息简洁明确**：提交信息应简洁、清晰，描述改动的原因，具体内容可以通过 本次请求上下文的修改以及 `git diff` 对比 结合得出
10. **分批次提交**：如果涉及到大量的未提交内容不要一次性全部提交，需要仔细查看所有未提交内容然后分类，分批次进行提交

---

## 十五、SVN → Git 速查对照

| 操作 | SVN | Git |
|------|-----|-----|
| 查看状态 | `svn status` | `git status` |
| 查看差异 | `svn diff` | `git diff` |
| 拉取更新 | `svn update` | `git pull` |
| 添加文件 | `svn add <file>` | `git add <file>` |
| 提交 | `svn commit -m "msg"` | `git commit -m "msg"` + `git push` |
| 查看日志 | `svn log -l 10` | `git log --oneline -10` |
| 撤销修改 | `svn revert <file>` | `git checkout -- <file>` |
| 解决冲突 | `svn resolved <file>` | `git add <file>` |
| 查看文件内容 | `svn cat -r REV <file>` | `git show HASH:<file>` |
| 查看某行是谁改的 | 无直接命令 | `git blame <file>` |
| 临时保存修改 | 无 | `git stash` |

> **关键区别**：Git 提交是本地的（`git commit`），需要 `git push` 才推送到远程；SVN 的 `svn commit` 直接提交到服务器。
