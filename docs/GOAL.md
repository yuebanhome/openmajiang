# OpenMajiang v0.1 最终目标

状态：实施中。规范来源为 [完整规格](v0.1-specification.md)。只有验收记录全部完成，才可宣称最终目标完成。

## 固定边界

- Go 后端、React/TypeScript 前端、PostgreSQL；Docker 打包。
- 国标麻将：144 张、81 番种、8 分不含花起和；完整 16 手及独立统计的 1/4 手练习。
- 真人、混合、Bot 三种模式；稳定身份、重连、幂等、事务恢复。
- 所有人均可匿名观战，但仅显示已经弃出的牌面；副露、摸牌、补花、终局成牌、暗杠及回放均不得扩大披露。
- 注册、验证、登录、找回、账号/设备管理、注销。
- Go 独立规则插件与注册表，能力不写死国标；测试插件验证扩展。
- 自托管 Bot、TS/Python SDK、内置 Bot、匹配、记录与公开弃牌复盘。
- GitHub Actions 的发布 job 绑定 Environment `DOCKERHUB`，读取 `secrets.USER`、`secrets.TOKEN`；PR 不读取发布凭据。
- 无支付、充值、钱包、房卡、兑换或付费配额。

## 实施与验收

追踪目标：[Issue #1](https://github.com/yuebanhome/openmajiang/issues/1)。首个实现：[PR #2](https://github.com/yuebanhome/openmajiang/pull/2)。

| 里程碑 | 实现位置 | 验收状态 |
| --- | --- | --- |
| M0 规格、插件 SDK、协议、骨架 | `pkg/rulesdk`、`rules/registry`、`api`、`docs` | 85 HTTP 操作及强 Schema 已落地，共同样例、生成一致性和离线校验通过 |
| M1 国标裁判、算番、赛程、重放 | `rules/mcr`、`rules/mcr/scoring` | 两种架构规则回归及 10,000 手已通过；保留线上裁决解释与专家复核边界 |
| M2 账号、房间、邀请、匹配 | `internal/auth`、`internal/platform` | 真实 PostgreSQL 权限、并发、恢复通过；新增修复须再跑 |
| M3 React、弃牌观战、重连、历史 | `web`、`internal/platform` | 构建和单测通过；43 前端单测通过；第七轮六组真实浏览器通过；本批锁等待修复待 CI |
| M4 内置/外部 Bot、SDK、统计 | `internal/bots`、`sdk`、`internal/platform/stats.go` | 两语言 SDK 用例通过；第六轮3 Python+1 TypeScript真实 WSS 112手通过；本批 Host 优化待复验 |
| M5 Docker、CI、运维、完整验收 | `Dockerfile`、`deploy`、`.github/workflows` | 双架构容器构建/冒烟通过；第七轮备份恢复通过；容量 ACK p95 184.351 ms / 裁决 p99 408.430 ms 超标，继续修复 |

实现落地与发布门槛分开记录。以上通过项只适用于验收记录标明的提交；后续变更由同一工作流重跑。

未通过的检查、未运行的环境验证和已知限制必须保留在 `docs/ACCEPTANCE.md`；不能用缩减规则替代完整国标。
