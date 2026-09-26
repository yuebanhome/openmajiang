# HTTP API 与平台运行契约

本文件对应 `internal/platform` 和 `internal/auth` 的实际路由。JSON 成功响应使用下表中的对象；错误统一为 `{"error":{"code":"…","message":"…"}}`。未知 `/v1/` 路由返回 JSON 404，不落入前端 SPA。

## 身份、来源和写入

- 浏览器账号使用 HttpOnly 会话 Cookie。`GET /v1/me` 提供本人资料和 CSRF token。浏览器写请求必须同源 `Origin`、`X-CSRF-Token`、`Content-Type: application/json`；游客观战票据不要求注册或 CSRF。
- 创建房间、入座、排队和创建 Bot 必须验证邮箱，账号状态必须 `active`。未验证账号可访问自己的设置、完成验证；游客可访问公开发现、观战和弃牌回放。
- Bot API Key 仅用于换取短期 Bot session，不能访问账号管理 API。短期 session 通过 `Authorization: Bearer …` 发送。任何密钥均不得放进 URL。
- 公开房间、公开快照、公开牌谱全部是弃牌视角。即使使用自己的账号 Cookie 或该桌 Bot 所有者身份访问公开接口，也不会升级为私人视角。
- 账号写接口按可信代理策略获得客户端地址。全局 HTTP、写请求、票据签发和 WS 消息有独立速率限制；WS 最多 2048 条、单桌最多 500 位观众、每地址 32 条、每控制身份 4 条。消息上限 32 KiB，发送阻塞 2 秒会断开慢消费者。

## 房间与入座

| 方法与路径 | 请求 | 响应与权限 |
|---|---|---|
| `GET /v1/public/rooms` | 无 | `{rooms:[Room]}`，最多 100 条，活跃优先 |
| `GET /v1/public/rooms/{id}` | 无 | `{room:Room}`，无需邀请码 |
| `GET /v1/rooms/{id}` | 无 | `{room,own_participant_id?}`，本人身份单独标识 |
| `POST /v1/rooms` | `{name,mode,ruleset_id,ruleset_version,match_format,online_profile?,seat_count?,invite_only,self_test}` | `{room,invite_code}`；邀请码仅本次返回，不存明文 |
| `POST /v1/rooms/{id}/join` | `{invite_code?:string}` | `{room,own_participant_id}` |
| `POST /v1/rooms/join` | `{invite_code:string}` | 查找邀请房并入座，返回房间 |
| `POST /v1/rooms/{id}/ready` | `{ready:boolean}` | 仅本人真人席位；返回房间 |
| `POST /v1/rooms/{id}/bots` | `{builtin:"random_legal"或"basic_heuristic"}` 或 `{bot_id:string}` | 房主添加内置或自己 Bot；返回房间 |
| `POST /v1/rooms/{id}/start` | `{}` | 房主开始；满座、全部准备、外部 Bot 在线且兼容后返回房间 |
| `POST /v1/rooms/{id}/leave` | `{}` | 等待中立即离席；比赛中记录手后退出 |
| `POST /v1/rooms/{id}/invite-rotate` | `{}` | 等待中的房主轮换邀请码，旧邀请码失效；观战不受影响 |
| `POST /v1/rooms/{id}/close` | `{}` | 房主关闭等待房，释放所有席位和等待房配额；不会中断进行中比赛 |
| `POST /v1/rooms/{id}/rematch` | `{}` | 已结束房间的房主新建同配置房间，返回 `{room,invite_code}`；其他参赛者重新同意入座 |
| `POST /v1/practice` | `{}` | 本人加三个内置 Bot，直接开始国标单盘练习，返回 `{room}` |
| `GET /v1/queue` | 无 | `{queued,room_id}`，本人匹配状态 |
| `POST /v1/queue` | `{ruleset_id,ruleset_version,match_format}` | `{status:"queued"}`，只匹配真人 |
| `DELETE /v1/queue` | 无 | `{status:"cancelled"}` |

`Room` 包含 `id/name/owner_id/mode/ruleset_id/ruleset_version/online_profile/match_format/capacity/invite_only/self_test/status/match_id/seats`。`seats` 仅给 `participant_id/bot_id?/name/kind/seat_id/ready/connected/leave_after_hand`，不返回账号邮箱、会话、控制凭证或私牌。房间等待时的 `seat_id` 是入座顺序；比赛中当前座位以授权快照和 `seat_assignment_version` 为准。

`mode` 为 `human_only/mixed/bot_only`。默认规则 `openmajiang.mcr@1.0.0`，默认标准 16 盘；练习使用 `practice_1/practice_4`。人数与线上补充从插件 manifest 选择，非法组合被拒绝。`self_test` 必须启用邀请入座，但依然公开观战。公共匹配同所有者最多一个身份，自测允许同一所有者多个独立 Bot。

公共匹配达到插件人数后，保留一个等待房间 60 秒，真人逐一准备，Bot 通过已鉴权 WS 发送 `ready`。全员准备后自动开始；确认超时取消房间并释放席位。不会静默用 Bot 填真人桌。

## 参赛、决策与恢复

| 方法与路径 | 响应 |
|---|---|
| `GET /v1/me/active-match` | `{room:null,match:null}` 或 `{room,match_id}` |
| `GET /v1/rooms/{id}/view` | 本人过滤快照及授权上下文，其他人的手牌不返回 |
| `POST /v1/rooms/{id}/take-control` | 增加本人控制世代，并返回新快照；旧连接不能继续控制 |
| `POST /v1/rooms/{id}/actions` | 完整 `submit_action` JSON 和 `X-Control-Token`；返回持久化 `command_ack` |
| `GET /v1/me/matches` | `{matches:[…]}`，本人及本人 Bot 的历史场次 |
| `GET /v1/me/matches/{id}` | 本人当前快照；Bot 视角通过 `?bot_id=…` 明确选择并校验归属 |
| `GET /v1/me/matches/{id}/replay?after=0&limit=100&hand_index=1` | `{frames,next_after,view_policy:"participant_private"}` |
| `GET /v1/me/hands/{id}/replay` | 同上，`hand_id` 为 `{match_id}_hand_{index}` |

浏览器动作还需要本标签页收到的 `X-Control-Token`；单独复制快照中的 epoch 不会获得控制权。动作需携带 `type/protocol_version/match_id/hand_id/participant_id/seat_id/seat_assignment_version/control_epoch/decision_id/window_id/command_id/option_id`。`protocol_version` 为 `1.0`，`command_id` 8–128 字符；`option_id` 必须来自本次合法动作。重试使用完全相同的动作对象，不能生成新的 `command_id`。

幂等范围 `(participant_id,match_id,command_id)`。在认证之后、当前窗口检查之前查询历史命令：同负载返回第一次结果，不同负载返回 `IDEMPOTENCY_CONFLICT`。`recorded` 表示响应意向已登记，不表示已经胜出；`applied` 表示直接动作已裁决。响应窗保持固定截止，多个玩家的登记互不使对方决策失效；时间到后统一裁决。

事务同时保存最新规则状态、输入和事件、每个参赛者过滤观测、独立弃牌投影及命令结果。事务提交前不会发送 ACK。回放数据也是这些已经持久化的过滤观测，不是事后重新生成的全知牌谱。

历史列表支持 `mode/ruleset_id/ruleset_version/match_format/status/bot_id` 过滤及 `limit`（默认25，最大100）、不透明 `before` 游标，响应包含 `next_before`。本人的 `bot_id` 筛选必须属于当前账号；翻页沿用相同过滤条件，不解析或修改游标。

## 匿名弃牌观战

| 方法与路径 | 响应 |
|---|---|
| `POST /v1/public/rooms/{id}/spectator-ticket` | `{ticket,expires_at,view_policy}`，5 分钟只读票据 |
| `POST /v1/public/matches/{id}/spectator-tickets` | 同上，按比赛找到房间 |
| `GET /v1/public/rooms/{id}/spectator` | `{type:"spectator_snapshot",view,room,match_id,hand_id,…}` |
| `GET /v1/public/matches` | `{matches:[{id,room_id,status,ruleset_id,ruleset_version,match_format,platform_interrupted,created_at}]}` |
| `GET /v1/public/matches/{id}` | 公开房间、比赛摘要和弃牌视图 |
| `GET /v1/public/matches/{id}/snapshot` | 公开弃牌快照 |
| `GET /v1/public/matches/{id}/replay?after=0&limit=100&hand_index=1` | `{frames,next_after,view_policy:"spectator_discard_only@1"}`；limit 最大 200 |
| `GET /v1/public/hands/{id}/replay` | 指定一手的公开弃牌回放 |

观众 `view` 是严格白名单，唯一牌面字段是 `discards[].kind`；discard 使用独立 `discard_id`。不能包含实体 tile ID、手牌、花牌牌面、副露牌面、番种、拆牌、墙序或随机种子。取用弃牌只改变 `claimed`，不展开手中消耗的牌。手终或比赛结束也不放宽。

公开流可包含座位展示名、身份类型、手牌张数、门风、圈风、剩余张数、动作名称、比分、终局方式和身份列表。`winners` 是为未来多赢家插件保留的纯参赛身份数组。

## WebSocket

- 玩家：`/v1/ws/players?room_id=…`，同源 Cookie。连接时重新核验账号和会话。无人控制时领取新世代，其他窗口有在线控制权时仅授予本人只读视角；显式接管后通过 `resume_control` 附着当前连接。
- Bot：`/v1/ws/bots?room_id=…`，短期 Bearer session；省略 room_id 则查询该 Bot 活动席位。
- 观众：`/v1/ws/spectators?room_id=…`，建立后 5 秒内首帧发送 `{"type":"authenticate","ticket":"…"}`。首帧通过前不发牌桌数据。

等待房间发送 `room_snapshot`。参赛快照线类型为 **`snapshot`**，并依次发送 `decision_request`；观众为 `spectator_snapshot`。每个连接得到独立 stream，跨手和场次重置。新手先发 `seat_assigned` 控制帧。完整快照带 `stream_id/view_seq`；决策的 `observation_ref` 精确引用刚发出的快照。`self_timeout_count/reaction_timeout_count` 只在本人授权快照中给出，用于接入验收。

输入支持 `hello/resume/resume_control/ready/submit_action/ping/heartbeat`。`control_granted` 一次下发本连接 `control_token`；同账号其他窗口不能通过普通快照取得它。`POST take-control` 返回新 token，随后 WS `resume_control` 携带该 token 恢复同一控制权；旧 token 被废止。`resume` 建立完整快照屏障，不延长截止时间；已登记意向放在快照 `recorded` 字段，不要求重复选牌。`ready` 只改变本人等待席位，观众无此权限。任何观众的 `submit_action` 返回 `READ_ONLY`。

服务端每 10 秒发送 `heartbeat`；`ping/heartbeat` 输入得到 `pong`。控制帧 `command_ack/command_error/error/seat_assigned/control_changed` 不通过状态序号去重。错误线类型为 **`command_error`**，内含 `command_id/error.code`。控制世代被取代的旧连接立即停止传输。每次参赛快照和动作前重新验证 session，撤销凭证会使既有连接失效。

## Bot 控制台与凭证

| 方法与路径 | 请求与响应 |
|---|---|
| `GET /v1/bots/{id}/status` | `{online,connected,presence,active_room_id,active_match_id,queued,continuous,last_error,last_error_at,recent_errors}`，仅本人；错误不含牌或动作负载 |
| `GET /v1/bots` | `{bots:[{id,name,enabled,current_version,online}]}`，仅本人 |
| `POST /v1/bots` | `{name}` → `{bot}`；每账号最多四个自定义 Bot |
| `PATCH /v1/bots/{id}` | `{name?,enabled?}`，停用会撤销现有凭证/session和控制世代 |
| `GET /v1/bots/{id}/versions` | `{versions:[{id,label,metadata,created_at}]}` |
| `POST /v1/bots/{id}/versions` | `{label,metadata?}` → `{version}`，新版本独立不可改写；场次锁定所选版本 |
| `GET /v1/bots/{id}/credentials` | 只列 `{id,created_at,revoked_at}`，不读取密钥 |
| `POST /v1/bots/{id}/credentials` | `{}` → `{credential_id,api_key,shown_once:true}`；仅这一次显示明文 |
| `DELETE /v1/bots/{id}/credentials/{credential}` | 撤销 key、关联 session，并增加控制世代 |
| `POST /v1/bots/{id}/queue` | `{ruleset_id,ruleset_version,match_format,continuous}`，所有者操作 |
| `DELETE /v1/bots/{id}/queue` | 取消排队及当前场次后的连续匹配 |
| `POST /v1/bot-sessions` | 长 key Bearer + `{protocol_version:"1.0",rulesets:[{id,version}]}` → `{session_token,expires_at,bot_id,protocol_version}` |
| `GET /v1/bot/active-match` | 短 session → `{room,match_id}` 或 `{room:null}` |
| `POST /v1/bot/queue` | 短 session，body 同 owner 队列接口 |
| `DELETE /v1/bot/queue` | 短 session，停止排队/连续匹配 |

`online` 依据 runner 最近 30 秒真实鉴权轮询／心跳，不能只凭尚未到期的 token 判在线；`connected` 表示座位 WS 连接租约，`presence` 为 offline/idle/seated/playing。空闲 runner 通过 active-match 轮询维持心跳，已入座连接每 10 秒响应 WebSocket Ping。

短 session 有效 30 分钟；SDK 在到期前更新、断线后重新换取并恢复。能力清单会存入 session，开局前要求包含房间锁定的规则版本。外部 Bot 不在添加时自动准备，必须真实连接后发送 `ready`。完成场次后 `continuous` 身份原子返回队列；规则、赛程、同所有者限制继续有效。

## 插件目录、运维与健康

- `GET /v1/rulesets`（别名 `/v1/public/rules`）返回 `{rulesets:[Manifest]}`；`GET /v1/rulesets/{id}/versions/{version}` 返回单版本。
- Manifest 声明人数、赛程、线上补充、接口及视图 schema、渲染器、功能和产物 hash。房间/Match 固定配置和 manifest，恢复时 hash 不匹配终止恢复，不能换引擎重新算历史分。
- 运维账户角色是 `operator`，通过部署 CLI `openmajiang admin grant --email … --reason …` 授予已验证账号；普通账号无提权接口。
- 运维与维护路由见 [operations.md](operations.md)，不提供改分或全知看牌接口。
- `GET /health/live` 检查进程存活，`GET /health/ready` 检查 PostgreSQL 及必需迁移；`openmajiang healthcheck` 调用 ready。
- `openmajiang migrate` 执行账号和平台增量迁移；`serve` 不自动建库、不创建默认账号。缺少数据库、独立 32 字节 Base64 密钥或 SMTP 配置时启动失败。
- `SIGTERM` 停止工作循环并限时关闭 HTTP；服务恢复通过数据库 owner/epoch 和表锁裁决。旧 owner 不得继续推进。同版本产物缺失、无效状态或停机超过 60 秒会结束当前未完成手，不补造分数。

常见错误包括 `AUTH_EXPIRED/ACCOUNT_NOT_ELIGIBLE/FORBIDDEN_SEAT/STALE_CONTROL/DECISION_CLOSED/ALREADY_SUBMITTED/INVALID_OPTION/IDEMPOTENCY_CONFLICT/ROOM_NOT_FOUND/INVALID_INVITE/TABLE_FULL/NOT_ALL_READY/BOT_OFFLINE_OR_INCOMPATIBLE/OWNER_ALREADY_SEATED_OR_QUEUED/MAINTENANCE/RULESET_DISABLED/RATE_LIMITED/CONNECTION_LIMIT`。鉴权错误重新认证，过期动作恢复快照，同一决策已提交则等结果；非法动作可以在原截止前修正，幂等冲突不能盲重试。


## 历史保留与过期

原始规则状态、动作幂等结果、受限事件、本人观测和弃牌逐帧牌谱统一保留至场次终止后的 30 天。后台启动时和每小时在 30 秒处理预算内按最多 100 场一批连续清理，只处理终态场次，活动桌不会被清理。每场清理在同一事务内保存不含牌面的最终分数/名次摘要并设置 `archived_at`，然后删除上述明细；规则版本、聚合统计和原有异常状态继续保留。

已归档比赛的 `/v1/public/matches/{id}` 返回 `archived:true/archived_at/summary/match`；历史列表也包含归档标记。其快照、私有视角、牌谱和历史动作重试统一返回 **410 `MATCH_ARCHIVED`**。这表示保留期届满，不是权限错误、仍在处理中或没有这场比赛。归档后不能重新裁判或补造原始牌谱。


## 免费资源容量

默认上限：每账号 4 个等待房间、平台 2048 个等待房间、200 场活动比赛、4096 个排队身份。通过 `MAX_OWNER_WAITING_ROOMS`、`MAX_WAITING_ROOMS`、`MAX_ACTIVE_MATCHES`、`MAX_QUEUED_PARTICIPANTS` 配置正整数。创建房间、排队、开局和匹配器在同一数据库准入锁内检查与写入，不能靠并发请求突破额度；同用户操作另有身份行锁。

达到上限分别返回 `WAITING_ROOM_LIMIT`、`ROOM_CAPACITY_REACHED`、`MATCH_CAPACITY_REACHED`、`QUEUE_CAPACITY_REACHED` 及可展示的解释。房主可关闭不使用的等待房，不能关闭已开始的比赛。连续 Bot 完成比赛后留下待排队记录，由匹配器在容量允许时领取，避免在持有牌局状态锁时反向等待准入锁；停止连续匹配会同步撤销这类待排队意图。
