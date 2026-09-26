# OpenMajiang

Go + React 的开放麻将平台。第一套规则为 144 张国标麻将，支持真人桌、人机混合桌、Bot 桌与自托管 Bot。无付费、钱包或房卡功能。

**观战对任何人开放，但只能看到已经弃出的牌面。** 登录、房主身份、比赛结束和回放都不会放宽观战权限；同场玩家通过独立鉴权接口获得自己的参赛视角。

v0.1 正在首个 PR 中集成。功能实现和测试结果分别记录在 [最终目标](docs/GOAL.md) 与 [验收记录](docs/ACCEPTANCE.md)，未验证项目不能视为发布就绪。

## 启动

需要 Docker Engine / Compose v2。

```sh
cp deploy/.env.example deploy/.env
# 按示例填写数据库密码、两个独立的随机密钥和 PUBLIC_BASE_URL。
docker build -t openmajiang:local .
# deploy/.env 中设置 OPENMAJIANG_IMAGE=openmajiang:local
docker compose --env-file deploy/.env -f deploy/compose.yaml --profile dev-mail up -d
```

本地访问 `http://localhost:8080`，开发邮件访问 `http://localhost:8025`。注册并验证邮箱后可入座；匿名用户可直接观战。生产环境使用 HTTPS 和真实 SMTP。完整参数、备份和发布步骤见 [部署说明](docs/deployment.md)。

## 开发

需要 Go 1.27.1、C/C++ 编译器、Node 24 和 PostgreSQL 17。国标算番使用 CGO，不能关闭 `CGO_ENABLED`。数据库不使用内存替代。

```sh
make build
make test
# 设置 DATABASE_URL 后：
go run ./cmd/openmajiang migrate
# 按部署文档设置邮件与认证参数后：
go run ./cmd/openmajiang serve
```

`TEST_DATABASE_URL` 启用真实 PostgreSQL 集成测试；没有该变量时会明确跳过这些用例。10,000 手规则门槛单独运行：

```sh
MCR_SOAK_MATCHES=625 go test ./rules/mcr -run '^TestSoak$' -count=1 -timeout=10m
go run ./cmd/bot-runner -local -hands 16 -strategy basic_heuristic
```

## 代码与约定

| 路径 | 作用 |
| --- | --- |
| `cmd/openmajiang` | HTTP / WebSocket 服务、迁移、健康检查 |
| `internal/auth` | 邮箱账号、会话、单次令牌、加密邮件 outbox |
| `internal/platform` | 房间、席位、对局事务、恢复、观战与 Bot 会话 |
| `pkg/rulesdk`、`rules/registry` | 确定性规则接口与显式注册 |
| `rules/mcr` | 国标状态机、144 张牌、8 分门槛、16 手赛程 |
| `rules/mcr/scoring` | 固定版本的 81 番算分与 WMO 适配 |
| `internal/bots`、`cmd/bot-runner` | 仅使用本人观测的基线策略和自托管客户端 |
| `web` | React / TypeScript 界面和视角隔离 |
| `deploy`、`.github/workflows` | Docker、备份恢复、验证与 Docker Hub 发布 |

- [产品与技术完整规格](docs/v0.1-specification.md)
- [规则插件开发](docs/rule-plugins.md)
- [规则来源、上游许可证与线上裁决](rules/mcr/scoring/NOTICE.md)
- [国标引擎与确定性说明](rules/mcr/README.md)

GitHub Actions 的发布 job 绑定已有 Environment **`DOCKERHUB`**，使用 **`USER` / `TOKEN`**。PR 只验证、不推送镜像；通过验证的 main / 版本标签才触发发布。仓库许可证为 MIT；引入的算番代码保留其原始 MIT 声明。
