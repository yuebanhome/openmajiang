# v0.1 验收记录

状态：本批代码已冻结，准备运行完整集成门槛。本文记录提交时已经取得的证据；本提交的最终检查结论以 PR 上对应 SHA 的 Actions 结果及报告为准。实现已进入 [PR #2](https://github.com/yuebanhome/openmajiang/pull/2)，尚未宣称生产发布就绪。目标见 [Issue #1](https://github.com/yuebanhome/openmajiang/issues/1)，门槛保留在完整规格第 13 节。

## 证据口径

测试文件存在、测试被跳过、某个页面能打开，都不算对应场景已通过。没有 `TEST_DATABASE_URL` 时，PostgreSQL 用例明确跳过；数据库验收以 GitHub Actions 的 PostgreSQL 17 服务为准。测试报告不包含凭据、牌墙或完整私有局面。

| 运行 | 精确提交 | 结果 |
| --- | --- | --- |
| [第一次 CI](https://github.com/yuebanhome/openmajiang/actions/runs/36234150607) | `ac84bbadfb888fd8c4cba9a382d78031481e7299` | amd64 / arm64 的前端构建、数据库测试、迁移、10,000 手规则模拟、Docker 构建及容器冒烟通过；浏览器步骤缺少脚本，整体失败 |
| [第二次 CI](https://github.com/yuebanhome/openmajiang/actions/runs/36234861079) | `e37def0d66a91a96befd7466cdba598c3b541aa9` | 两种架构的上述步骤及扩展 PostgreSQL race / 恢复测试通过；浏览器注册标签、WSS 开局控制代数失败，整体失败；备份恢复未执行 |

[第三次 CI](https://github.com/yuebanhome/openmajiang/actions/runs/36236305507) 对提交 `b69475b02dcacf791fa160ebb1240ddb2e740420` 的两种架构均通过：85 接口生成契约、SDK、Go race（含真实 PostgreSQL 的新增控制、配额、统计、归档及混合 16 手）、重复迁移、10,000 手模拟、原生镜像构建与容器冒烟。浏览器已经完成注册、验证、出牌、匿名视角及刷新；Chromium 在 Bot 版本名称的精确文本断言失败，trace 证明接口 201 且版本已可见，测试已据真实结构修正。WebKit 在私有回放硬导航时原生页面崩溃，尚无证据确认根因，未跳过该项目。另修复已证实的内联字体被 CSP 阻断，继续保持严格 CSP；下一轮收集浏览器原生诊断日志。随后核对 [Playwright 官方 issue #37766](https://github.com/microsoft/playwright/issues/37766) 及 [维护者修复说明](https://github.com/microsoft/playwright/issues/37766#issuecomment-3513156220)：1.56 所带 WebKit 存在刷新/导航原生崩溃，修复包含于 1.57；测试依赖升级并精确锁定到 1.57.0（npm ci、typecheck、43 单测、build 和三个浏览器项目收集通过）。同时修复 Bot 队列按钮以真实 online 字段判断运行端状态，覆盖仅轮询在线、已禁用和管理员停用场景。当前症状与上游回归一致，但仍以升级后的同一完整场景实跑验证，不提前宣称问题消失。

第三次运行的 WSS 与容量长测不计为通过；后续代码推送会取消旧运行。下一批同时修复管理员停用后残留的连续匹配意图、添加归档查询必要索引，保持备份恢复的数据非空守卫，并允许其在浏览器断言失败后独立执行。

第二次 CI 的容量 job 虽显示绿色，当时提交尚无容量测试函数，因此**不计为容量通过**。后续门槛增加测试存在性、报告存在性、`passed=true` 及实测时间至少 3,600 秒的断言，防止空跑通过。

## 分项覆盖与待验证

| 范围 | 已有实现与检查 | 当前结论 |
| --- | --- | --- |
| R01–R06、R08–R10 国标状态机 | 144 张物理牌守恒、起手/手内补花、所有杠及抢杠、末墙、8 分门槛、支付、身份换座、16 手、确定性重放 | 两种架构 CI 规则与 10,000 手门槛通过；后续新增代码仍需同一工作流重跑 |
| R07 81 番与最优解释 | 固定上游提交与 WMO 适配、81 正例、原文算例、排除关系、特殊结构、191 组上游回归、真实最高分拆分、并发 | 81 正例及 81 个合法成牌反例通过；420 万次算番 p95 9.224 µs、最大 37.181 ms（amd64，见 PERFORMANCE.md）；未声称获得独立国标裁判认证 |
| P01–P04、D01–D02 事务/恢复 | ACK 丢失重试、同 ID 冲突、首次有效响应、截止竞争、单写者 fencing、保留登记响应、hash 不兼容拒绝、平台中断 | 两架构真实 PostgreSQL race 测试通过；第二窗口/等待控制代数新增回归待本提交 CI |
| S01–S03、W01–W03 视角与权限 | 独立弃牌白名单、跨席拒绝、公开 REST/WS/回放、100 次隐藏牌扰动、缓存隔离、注销后历史去名 | 已有 Go/React 用例通过；新增真实浏览器和终局归档回归待本提交 CI |
| A01–A03 邮箱账号 | 验证/找回单次令牌、密码/邮箱修改、会话撤销、加密邮件 outbox、注销、事务竞态、CSRF | 数据库用例通过；浏览器注册标签缺陷已修复并补回归，待真实浏览器重新贯通 |
| Q01 房间生命周期 | 身份占用、准备/开局、取消排队、房主转移、再来一场创建新房保留历史 | 已实现并补事务配额；非成员退出与终局席位保留缺陷已修复，待数据库回归 |
| U01 真人与混合 | 真实 Host 4 人 16 手及身份映射测试、短练习、React 操作/断线状态 | 4 真人协议/事务测试通过；新增 1 真人+3 Bot 完整 16 手及三个浏览器/视口项目待 CI；前端 43 单测、类型检查及构建通过 |
| B01–B02 Bot | 两种 SDK 共同样例、快照屏障、超时取消、ACK 跟踪、自动匹配、会话续期、凭据撤销；Go 基线 Bot 本地 16 手 | SDK 用例通过；真实 100 手 WSS 第一次运行 0 手即发现等待快照 control_epoch 缺失，已修复，使用 3 个 Python + 1 个 TypeScript 独立客户端重新测量；不缩短生产时钟 |
| G01–G03 插件 | 静态注册、冻结实际可执行产物 SHA256（另存源码身份）及 config/schema、三人双赢家连庄测试插件、恢复/投影 | 相同 Host 真实数据库测试通过；toy 不注册到生产目录 |
| P0 统计 | 16 项指标、样本量、精确有理数标准分、冻结版本/模式/时钟维度、异常/自测隔离、个人与 Bot 授权 | 纯逻辑测试通过；新增统计 PostgreSQL 用例待下一 CI |
| 开放契约 | OpenAPI、JSON Schema、llms 文档、Go/React/TS/Python 共用合成样例、生成漂移检查 | 85 个实际 HTTP 操作、93 个协议 Schema 已落地；Go 路由覆盖、离线 Schema 正负例、生成一致性及两语言 SDK 各 8 用例通过，CI 强制复查 |
| W04 容量 | 20 真人时钟桌+30 Bot 标准桌、200 席、至少 1,000 健康观众、一桌 500 热点、10 慢消费者；ACK p95 / timer p99 | 实现真实一小时 gate，尚未取得有效一小时报告；新增先接新流再关旧流的换桌驱动回归已通过；无容量承诺 |
| C01–C02 构建/发布 | amd64 与 arm64 原生构建/运行；PR 不绑定发布环境；publish 使用 DOCKERHUB / USER / TOKEN；版本不可覆盖 | 两架构容器构建/冒烟已通过；真实 Docker Hub 推送只能由合并后的 main / 版本发布任务验证，尚未执行 |
| C03 运维与保留 | operator 授权/审计、维护、禁用规则/账号/Bot、故障中止、30 天终局归档、SIGTERM 排空、备份/空库恢复脚本 | 管理数据库测试已在 CI 运行；归档与备份恢复新增检查待通过，未宣称已演练回滚 |

## 可复现命令

环境：Go 1.27.1，CGO/C++，Node 24，PostgreSQL 17。CI 分别使用原生 `ubuntu-24.04` / `ubuntu-24.04-arm`。环境配置和容器启动见 [部署文档](deployment.md)。

```sh
cd web
npm ci
npm run typecheck
npm test
npm run build
cd ..
go vet ./...
go test -race -count=1 ./...
MCR_SOAK_MATCHES=625 go test ./rules/mcr -run '^TestSoak$' -count=1 -timeout=10m
go run ./cmd/bot-runner -local -hands 16 -strategy basic_heuristic
```

没有数据库变量的本机命令不代替 CI 数据库结果。API 生成和校验见 [api/README.md](../api/README.md)，外部 WSS 及固定依赖见 [sdk/integration/README.md](../sdk/integration/README.md)。一小时容量必须显式设置 `OMJ_CAPACITY_SECONDS=3600`、`OMJ_CAPACITY_REPORT` 和真实数据库地址；短运行不能标记验收通过。

## 规则解释与发布边界

规则来源锁定 WMO 2014 第二版，线上补充锁定 `om-mcr-1`；来源、许可证、上游适配及“多杠时加计最高暗杠类别”的明确解释见 [scoring/NOTICE.md](../rules/mcr/scoring/NOTICE.md)。规则牌例与程序审查不等于外部裁判认证。复杂拆分穷举范围、测量机器及原始数据见 [PERFORMANCE.md](../rules/mcr/scoring/PERFORMANCE.md)；三家同时可和的 1,000 次裁决加五个投影实测 p95 1.703 ms、最大 4.636 ms，不含数据库/网络。后续行为变更必须升版，不能静默改写历史。

CI WebKit / Chromium 和移动视口验证仅代表对应浏览器引擎的自动化结果，不等于真实 iPhone / Android 设备实测。容量报告会记录机器、同机客户端负载、局数、连接数及延迟，不把局域测试延迟等同于公网体验。

PR 尚未合并，不存在已部署服务器或已验证 Docker Hub 发布的声明。所有未通过或尚未执行的门槛必须继续保留，不能只因实现完成就勾选。
