# 自托管 Bot SDK

TypeScript（Node.js 24+）和 Python（3.11+）共享 `fixtures/decision.json` 协议用例。
两者连接平台提供的 Bot HTTP / WebSocket 接口，策略和 LLM 调用都在 Bot 所有者
自己的进程运行。平台不接收源码或托管任意程序。

先完成邮箱验证，在账号页创建 Bot、不可变版本和运行凭证。长期凭证只展示一次；
运行时通过环境变量注入，不写入仓库、日志、命令行参数或 URL。SDK 使用
`POST /v1/bot-sessions` 换短期会话，后续 HTTP 与 WSS 均用 Authorization header。
公开部署必须 HTTPS / WSS；仅 localhost 测试可用明文连接。

## TypeScript

```sh
cd sdk/typescript
npm ci
npm test
npm run build
```

在该目录创建 `run.mjs`：

```js
import { BotClient, firstLegal } from './dist/index.js';
const stop = new AbortController();
process.once('SIGINT', () => stop.abort());
const client = new BotClient({
  baseURL: process.env.OPENMAJIANG_URL,
  apiKey: process.env.OPENMAJIANG_BOT_KEY,
  queue: {
    ruleset_id: 'openmajiang.mcr', ruleset_version: '1.0.0',
    match_format: 'practice_1', continuous: true,
  },
}, firstLegal);
await client.run(stop.signal);
```

可将 `firstLegal` 替换为 `async ({observation, decision, signal, deadline}) => optionID`。
必须返回本次 `decision.legal_actions` 中的 `option_id`。调用支持取消的 LLM / HTTP
客户端时传入 `signal`。快策略应在预算内返回；慢推理超时后不能继续提交旧结果。
`deadline` 为扣除安全余量后的本机毫秒时间。别从长期缓存中替换当前观察状态。

## Python

```sh
cd sdk/python
python3 -m venv .venv
. .venv/bin/activate
pip install -r requirements.lock
pip install --no-deps -e .
python -m unittest discover -s tests -v
```

```python
import asyncio
import os
from openmajiang import BotClient, first_legal

async def main():
    stop = asyncio.Event()
    bot = BotClient(
        os.environ['OPENMAJIANG_URL'], os.environ['OPENMAJIANG_BOT_KEY'], first_legal,
        queue={'ruleset_id': 'openmajiang.mcr', 'ruleset_version': '1.0.0',
               'match_format': 'practice_1', 'continuous': True})
    await bot.run(stop)

try:
    asyncio.run(main())
except KeyboardInterrupt:
    pass
```

策略可为普通轻量函数或 `async def choose(context)`。`context.deadline` 是本机
`time.monotonic()` 时基，`context.cancelled` 是取消事件；不要在事件循环中做阻塞
网络或大量计算。异步请求必须支持取消，CPU 推理应交给有超时终止能力的独立进程。

## 协议与恢复

- SDK 先查询自己的 active-match；没有席位时可按配置申请队列。省略 `queue` 时只
  等待房主邀请入座。收到等待房间快照后自动发送 ready。`continuous` 控制赛后继续
  匹配；停止进程前可显式 `leaveQueue()` / `leave_queue()` 取消等待队列。
- 每次决策必须有匹配的最新 snapshot：stream、view_seq、match、hand、participant、
  seat assignment、control epoch 全部匹配。缺状态时发送 resume；ACK 和 heartbeat
  从不推进观察序列。
- 策略只收到当前 Bot 的授权视角；SDK 不提供全知观察或其他座位手牌。只剩 pass
  时 SDK 静默提交且不调用策略。默认自动补花，可用 `autoFlower:false` / 
  `auto_flower=False` 关闭，让策略自行选择合法补花或出牌动作。
- command_id 每次新决策随机生成。未收到 ACK 时原样重发同一个完整命令；不改变
  option、epoch 或 command_id。`recorded` 仅表示服务器已锁定回应，不能当作已经
  执行吃碰杠。以随后快照为准。
- 断线、控制变化或新快照立即取消旧推理。指数退避带抖动；重连后先恢复快照再
  重试旧命令。会话接近到期时重新换取短期凭据并恢复连接。迟到推理不会生成动作。
- SDK 当前使用进程内 pending journal。连接中断可保留 command_id，进程重启后依靠
  服务器快照的 recorded receipt 恢复；不把本地缓存当作结算事实。平台对同一
  decision 的首个有效回应唯一约束仍是最终防线。

`firstLegal` / `first_legal` 是连通性基线，优先胡牌再选可出的牌，不代表国标麻将
竞技水平。插件支持的规则 ID / 版本必须通过 `rulesets` 与队列配置明确声明；未来
不同规则应使用各自策略与观察类型，不能只改显示名称就复用假设。

## 验收范围

TS 和 Python 都验证快照屏障、ACK 独立序列、旧席位/epoch 拒绝、固定命令重试、
only-pass、不自动补花选项、取消后迟到结果，以及过期/非法动作不提交。
这类 fixture 测试不替代真实比赛。发布门槛另外包括真实 PostgreSQL、4 个外部 Bot
进程、WSS、连续 100 手、断线重连、撤销凭据、断点恢复和至少一次完整16手赛程。
运行结果必须记录构建 SHA、规则版本、是否发生平台中断、完成手数、超时与非法
动作计数；不能把“客户端启动成功”报告为100手验收通过。
