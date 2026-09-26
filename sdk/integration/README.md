# 默认时钟的真实 WSS 验收

`wss-smoke.py` 从公共 API 注册4个独立账号，在 Mailpit 收到真正经 SMTP 发出的
验证邮件后完成验证，再分别创建 Bot 和凭证。测试启动4个独立 Python Bot 进程，
通过验证服务器证书的 WSS 连接公共匹配队列；不使用数据库资格注入、不修改牌墙、
不缩短行牌或回应时钟。

CI 的 `deploy/ci-wss.sh` 为 localhost 生成临时 CA 和服务端证书，启动 Caddy TLS
代理；`SSL_CERT_FILE` 让 urllib 和 websockets 使用该 CA。测试禁止 `verify=False`
及明文 WS。邮件 API 依据 [Mailpit 官方 API](https://mailpit.axllent.org/docs/api-v1/)。

```sh
SSL_CERT_FILE=/path/to/ci-ca.pem .venv-sdk/bin/python sdk/integration/wss-smoke.py \
  --base-url https://localhost:18443 --mailpit-url http://127.0.0.1:8025 \
  --hands 100 --format standard_16 --timeout 6900 \
  --report test-results/bot-wss.json
```

只有4名参赛者都看到同一个、零和的手末结算，才计为完成一手。`standard_16` 会
等最后一场完整比赛结束，所以请求100手通常实际完成112手；不能把112次动作或
连接误称为112手。500ms固定回应窗使测试可能超过一小时，独立CI作业留有构建和
运行余量，报告实际耗时。快速尝试可用 `--hands 1 --format practice_1`，这不满足
100手及完整16手门槛。

脚本还会：

- 主动断开一次中途 Bot 连接，并验证重连成功；客户端临近短会话过期时续期。
- 检查匿名 HTTP 和 WSS 观战投影，以及完整分页的赛后公开回放，只允许弃牌牌面。
- 核对本人可见的服务器 self/reaction timeout 计数；默认总上限12仅为主动断线和
  后续正常凭据续期的有限容错，同时记录精确数量。
- 拒绝非法选项、席位越权、幂等冲突、策略异常、非正常终局及平台中断。
- 最后为各 Bot 创建一个新的短会话，先确认可用，再撤销其长期凭据，核对旧短会话
  和长期凭据均立即不可用。凭据及邮件令牌都不写入报告。

报告包括构建 SHA、规则版本、规则时钟档、真实完成手数／场数、4进程统计、连接
次数、匿名视图检查数量、服务器超时、命令错误和撤销验证结果。测试未执行或未
达到门槛时不得将报告标记通过。测试账号只能对专用验证环境创建；不要对生产
Mailpit或真实用户邮箱运行此脚本。
