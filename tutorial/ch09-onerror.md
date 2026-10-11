# 第 9 章 失败怎么办：onError 错误链路

> **目标**：压测中错误是常态。流程要对每种失败有显式设计：哪些忍、哪些重试、哪些恢复、哪些止损。掌握 onError 四件套与错误码体系。
>
> **前置**：[第 5 章](ch05-first-flow.md)、[第 8 章](ch08-listens.md)。示例 `examples/ch09/`：连接重试后中断、签到失败跳过、特定业务码忽略、战斗失败走恢复分支。

## 9.1 先认识错误码

两段式（第 4 章提过码段契约）：

- **框架码 < 100**：工具自产。日志里最常见的：

| 码 | 含义 | 什么时候出现 |
|---|---|---|
| 1 | 连接未建立 | 动作要用的 service 还没连上 |
| 2 | 连接失败 | 拨号被拒/断开 |
| 4 | 等响应超时 | 请求发出后 timeout 秒内没等到 |
| 22 | 必填绑定取不到值 | state 系绑定缺失（第 7 章隐式必填） |
| 24 | 动作中断（abort 的收束码） | strategy=abort 最终失败时，用它包住原错误 |
| 31 | 监听等待超时 | tcpListen 等满（第 8 章） |
| 6 | 动作被取消 | 停止机器人时在途动作——**不算故障** |

- **业务码 ≥ 100**：服务器在协议头 errorCode 里返回的，中文描述来自 errors.json。
- 码 5 已退役不复用（历史档案里可能还有）。

## 9.2 onError 四件套与固定顺序

`onError` 配在 **action 节点**上（不是动作定义上）：

```json
"connect": {
  "type": "action",
  "action": "ConnectArena",
  "onError": { "retry": { "maxRetries": 2, "retryDelayMs": 500 }, "strategy": "abort" }
}
```

四件套：`ignoreCodes` / `handler` / `retry` / `strategy`。执行顺序**固定**，理解顺序就理解了全部：

```
动作失败
  → 是"取消"？直接返回（下面全部不走）
  → 命中 ignoreCodes？warn 并继续流程（handler/retry/strategy 全部短路）
  → 执行 handler（若配了）
  → 还有重试余额？等 retryDelayMs → 重跑动作（回到最上面）
  → 最终失败：按 strategy 收束
```

## 9.3 ignoreCodes：点名的失败当没发生

```json
"buyItem": {
  "type": "action", "action": "BuyItem",
  "onError": { "ignoreCodes": [2001, 3002], "strategy": "resume" }
}
```

命中列表中的码 → 打 warn `动作错误码已忽略，流程继续`，流程继续走。三个特性要记住：**监控仍保留失败样本**（数据不撒谎）；**listenRefs 照常注册**；后面的 handler/retry/strategy 全部不再执行。适合"服务器明确说不行，但业务上可接受"的业务码。

## 9.4 retry：多试几次

`maxRetries` 是**额外**次数（配 2 = 最多执行 1+2 次）；`retryDelayMs` 每次重试前协作等待。重试期间成功就算成功——但 monitor 里每次尝试独立记样本，中间的失败不回滚。实测日志（无服务器跑示例）：

```
[ENGINE] 动作失败后准备重试  retryUsed=1 maxRetries=2 retryDelayMs=500 errorCode=2
[ENGINE] 动作失败后准备重试  retryUsed=2 maxRetries=2 retryDelayMs=500 errorCode=2
[ENGINE] 流程异常退出        error=[24] action=ConnectArena: [2] service=arena address=...
```

两次重试间隔精确 500ms；重试耗尽后走 strategy（示例是 abort）。

## 9.5 handler：失败后的恢复分支

```json
"battleOnce": {
  "type": "action", "action": "BattleOnce",
  "onError": { "handler": "recover", "strategy": "resume" }
},
"recover": { "type": "sequence", "next": ["reconnect", "noteFailure"] }
```

- `handler` 是一个**节点 ID**，以调用边执行那棵子树（不写进任何 next，流程图上它是"悬挂"的恢复分支）；
- 执行完 handler 回到本节点的失败处理流程（继续判断 retry/strategy），**不是**跳到 handler 继续；
- handler 在**每次失败尝试后都会执行**（重试之间也会跑）；
- handler 子树自身失败：错误直接上抛取代原错误（不再享受本节点的 retry/strategy）；handler 里触发 skip 会被吞掉视为已处理。

典型用法：网络类失败 → handler 里重连 + 重登录，然后 retry 原动作。

## 9.6 strategy：最终失败的收束

| strategy | 行为 |
|---|---|
| `resume`（默认，含不配置） | 当没发生，继续下一节点 |
| `skip` | 结束当前分支：由外层容器捕获——sequence 跳过剩余兄弟、loop 退出循环、boolean/switch/weighted 正常完成 |
| `abort` | 中断整个流程：错误码 24 包装原错误上抛，`[ENGINE] 流程异常退出`（error 级），机器人结束 |

skip 的两个注意点：**wait 节点不捕获 skip**；若 action 节点顶层直挂（外层没有容器），skip 冒到入口即"流程异常退出"——skip 需要至少一层容器接住。示例的 `signin` 配 skip：签到失败 → `claimMail` 也被跳过 → `dailyOptional` 整段完成，主流程继续 `dailyRoutine`——"这一组日常任务是可选的"语义一行配置就出来了。

abort 的语义是止损："连不上服务器，后面的动作全是连锁失败，别制造垃圾流量"。这正是第 5 章末尾那条 2→1→22 失败链的正确解法——上游失败后不要继续执行依赖请求。

## 9.7 取消：停止不是故障

停止一批机器人时，在途动作报码 6，监控归类 **Canceled**（不算 Timeout/Failure）。日志里停止阶段可能看到 detail 带 `stopping:` 前缀的取消——都是正常收尾，别当故障排查。取消不进任何 onError 环节（重试一个正在被停的动作没有意义）。

## 9.8 运行与观察

```bash
go run ./cmd/stressbot -config tutorial/examples/ch09/stressbot.toml
```

无服务器时整条链是教材：connect 重试 2 次（500ms 间隔）→ abort 中断（上面 9.4 的实测日志就是本示例产出）。想看 ignoreCodes/skip/handler 的现场，注释掉 connect 节点的 onError（改默认 resume）再跑：buyItem 报 22 不在忽略列表照常失败、signin 失败触发 skip 跳过 claimMail、battleOnce 失败进 recover 重连。有服务器时（【需目标服务器】）配合服务器返回 2001/3002 观察忽略链路。

## 9.9 练习与验收

练习：把 `signin` 的 strategy 从 skip 改成 abort，无服务器重跑，观察流程在第几个节点停止（签到失败即整流程中断，mail/商店/战斗全不执行）。

- [ ] 能背出四件套的执行顺序：取消 > ignoreCodes > handler > retry > strategy
- [ ] 能说出 ignoreCodes 命中后"流程继续但样本保留、监听照常注册"
- [ ] 知道 skip 由谁捕获、abort 的错误码是 24、取消是 6 且不算故障
- [ ] 会为"可选任务组"和"连接失败止损"分别选出正确的 strategy

## 延伸阅读

- 错误码全表：[docs/error-code-system.md](../docs/error-code-system.md)；分布式下的异常语义：[docs/distributed-error-handling.md](../docs/distributed-error-handling.md)
- 下一章：[第 10 章 心跳与连接保持](ch10-heartbeat.md)
