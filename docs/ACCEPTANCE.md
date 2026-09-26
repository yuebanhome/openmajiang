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

[第四次 CI](https://github.com/yuebanhome/openmajiang/actions/runs/36237268078) 对提交 `6d651cba7fe7c61857add6bc44f3b1cc24e9cb45` 的 amd64 / arm64 完整验证任务均通过，包括全部数据库回归、每架构 10,000 手、原生容器、桌面及移动 Chromium / 移动 WebKit 三项目（共六组）、真实 PostgreSQL 非空备份恢复、重复迁移、SIGTERM 退出 0 和重启健康检查。WebKit 保留了原先失败的刷新至本人回放导航，并通过后续 Bot 凭据与密码重置流程。

该次容量运行取得首份有效一小时报告，但**延迟不达标**：4 核 AMD EPYC 7763、Go 1.27.1、服务与负载发生器同进程；实测 3,600.217 秒，50 桌 / 200 席，最低 80 玩家 WebSocket / 1,110 健康观众，完成 1,030 手及 32 场。协议错误 0、人口下降 0，10 个慢消费者全部释放，热点及单 IP 限额有效；47,949 个 ACK 样本的 p95 为 587.904 ms，78,550 个定时裁决样本的 p99 为 1,084.645 ms，超过原有 100 ms 门槛。进程 CPU 11,105.144 秒（含客户端），峰值 RSS 297,582,592 字节，数据库由 8.7 MB 增至约 3.06 GB。未把长测执行完成当成容量通过。

原始失败数据保存在 [容量报告](evidence/ci-36237268078-capacity.json)。下一批针对已找到的重复 JSON 解析、数据库往返和调度等待进行优化，并增加 CPU / heap profile、连接池等待、完整分位与分钟进度取证。保持一小时、桌席/观众人数、生产时钟和延迟阈值不变；优化效果仍须实测确认。

第四次 WSS 验收已通过，见 [原始报告](evidence/ci-36237268078-bot-wss.json)：4 个实际注册及验证的账号，3 个 Python 和 1 个 TypeScript 独立客户端，在生产默认时钟下完成 112 手 / 7 场 `standard_16`，用时 5,676.35 秒；匿名隐私检查 112 次，公开回放分页检查、主动断线重连与 4/4 凭据级联撤销通过。记录到 2 次回应超时，以及重连中的 1 次 `STALE_CONTROL` 拒绝，不能写成所有错误均为零；没有非法选项、越席操作、幂等冲突或平台中断。报告中的实际构建 SHA `bfae49d685c235c29ff6f264a643703f5323e9ab` 是 Actions 的 PR 合并候选，其树与 PR 头 `6d651cba7fe7c61857add6bc44f3b1cc24e9cb45` 均为 `0b4bcf76c41c4ebf1d294b3f964fb86464e0dc8f`。容量仍不通过，因此整轮 CI 不能计为成功。

性能修复已通过本地全仓 Go vet/race、263 组合法观察的 Bot 动作排序等价回归、公共缓存与真实 WebSocket 的视角隔离回归；Go/Python 契约重新生成无漂移。公开查询合并为一个 MVCC 读取（不获取规则私有状态），持久化批量执行且仍在原事务内，调度保留原 8 桌并发并隔离慢桌，鉴权继续逐次读取有效性。同种弃牌计算复用的配对数据见 [Bot 性能说明](../internal/bots/PERFORMANCE.md)。新增 PostgreSQL 回归在本地明确跳过，待下一轮真实 CI；这些微基准不能代替容量验收。

## 分项覆盖与待验证

| 范围 | 已有实现与检查 | 当前结论 |
| --- | --- | --- |
| R01–R06、R08–R10 国标状态机 | 144 张物理牌守恒、起手/手内补花、所有杠及抢杠、末墙、8 分门槛、支付、身份换座、16 手、确定性重放 | 两种架构 CI 规则与 10,000 手门槛通过；后续新增代码仍需同一工作流重跑 |
| R07 81 番与最优解释 | 固定上游提交与 WMO 适配、81 正例、原文算例、排除关系、特殊结构、191 组上游回归、真实最高分拆分、并发 | 81 正例及 81 个合法成牌反例通过；420 万次算番 p95 9.224 µs、最大 37.181 ms（amd64，见 PERFORMANCE.md）；未声称获得独立国标裁判认证 |
| P01–P04、D01–D02 事务/恢复 | ACK 丢失重试、同 ID 冲突、首次有效响应、截止竞争、单写者 fencing、保留登记响应、hash 不兼容拒绝、平台中断 | 第四次双架构真实 PostgreSQL race 通过；本批调度及同进程租约过期新回归待 CI |
| S01–S03、W01–W03 视角与权限 | 独立弃牌白名单、跨席拒绝、公开 REST/WS/回放、100 次隐藏牌扰动、缓存隔离、注销后历史去名 | 第四次数据库、六组浏览器及 112 手 WSS 公开视图检查通过；本批单 SQL 与缓存优化待完整复验 |
| A01–A03 邮箱账号 | 验证/找回单次令牌、密码/邮箱修改、会话撤销、加密邮件 outbox、注销、事务竞态、CSRF | 第四次数据库及六组真实浏览器通过；本批鉴权往返优化的新 PG 用例待 CI |
| Q01 房间生命周期 | 身份占用、准备/开局、取消排队、房主转移、再来一场创建新房保留历史 | 第四次配额、非成员退出、终局历史保留及浏览器回归通过；本批离席快照等价检查待 PG 复验 |
| U01 真人与混合 | 真实 Host 4 人 16 手及身份映射测试、短练习、React 操作/断线状态 | 第四次两架构的 4 真人及 1 真人+3 Bot 完整 16 手、六组浏览器通过；43 前端单测、类型检查及构建通过；本批 Host 优化待 CI |
| B01–B02 Bot | 两种 SDK 共同样例、快照屏障、超时取消、ACK 跟踪、自动匹配、会话续期、凭据撤销；Go 基线 Bot 本地 16 手 | 第四次真实 112 手 / 7 场 WSS 通过，3 Python+1 TypeScript；2 次回应超时、1 次旧控制拒绝如实保留；本批 Host 性能变更后将完整复跑 |
| G01–G03 插件 | 静态注册、冻结实际可执行产物 SHA256（另存源码身份）及 config/schema、三人双赢家连庄测试插件、恢复/投影 | 相同 Host 真实数据库测试通过；toy 不注册到生产目录 |
| P0 统计 | 16 项指标、样本量、精确有理数标准分、冻结版本/模式/时钟维度、异常/自测隔离、个人与 Bot 授权 | 第四次纯逻辑及双架构 PostgreSQL 用例通过；本批同事务批量写入待复验 |
| 开放契约 | OpenAPI、JSON Schema、llms 文档、Go/React/TS/Python 共用合成样例、生成漂移检查 | 85 个实际 HTTP 操作、93 个协议 Schema 已落地；Go 路由覆盖、离线 Schema 正负例、生成一致性及两语言 SDK 各 8 用例通过，CI 强制复查 |
| W04 容量 | 20 真人时钟桌+30 Bot 标准桌、200 席、至少 1,000 健康观众、一桌 500 热点、10 慢消费者；ACK p95 / timer p99 | 第四次实测完整一小时，人数/隐私/慢消费者通过，但 ACK p95 587.904 ms / timer p99 1,084.645 ms 超标；性能修复待原负载复测，无容量通过声明 |
| C01–C02 构建/发布 | amd64 与 arm64 原生构建/运行；PR 不绑定发布环境；publish 使用 DOCKERHUB / USER / TOKEN；版本不可覆盖 | 两架构容器构建/冒烟已通过；真实 Docker Hub 推送只能由合并后的 main / 版本发布任务验证，尚未执行 |
| C03 运维与保留 | operator 授权/审计、维护、禁用规则/账号/Bot、故障中止、30 天终局归档、SIGTERM 排空、备份/空库恢复脚本 | 第四次双架构管理/归档、非空数据库备份恢复、重复迁移、SIGTERM 退出 0 与重启健康检查通过；未宣称已完成生产版本回滚 |

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
