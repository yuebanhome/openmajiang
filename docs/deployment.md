# 部署、构建与版本发布

## 运行组成

应用镜像包含 Go 服务、嵌入的 React 静态文件和 `bot-runner`，不需要生产 Node 进程。Go 国标算番通过 CGO 调用 C++，镜像保留 `libstdc++6`。应用使用 UID/GID 10001，不以 root 运行。PostgreSQL 独立持久化，Compose 不暴露数据库端口；首版只运行一个应用实例。

本机开发需要 Go 1.27.1、C/C++ 编译器、Node 24、npm；容器部署需要 Docker Engine 与 Compose v2。数据库使用 PostgreSQL 17。执行 `make build` 会先构建前端，再把前端嵌入两个 Go 二进制。前端生产请求与 API 同源。构建参数没有 SMTP、数据库、Bot 或 Docker Hub 凭据。

## 首次启动

1. 复制 `deploy/.env.example` 为 `deploy/.env`。数据库密码建议使用 `openssl rand -hex 32`，避免数据库 URL 中的保留字符；`AUTH_MAIL_KEY` 和 `TOKEN_HASH_KEY` 分别使用一次 `openssl rand -base64 32`，必须是不同的密钥。不要提交 `.env`。
2. 本地测试可以 `docker build -t openmajiang:local .`，并设置 `OPENMAJIANG_IMAGE=openmajiang:local`。公开部署从成功 Actions 的摘要复制 `docker.io/<账号>/openmajiang@sha256:...`，不要依赖可变的 `main` 或 `latest` 标签回滚。
3. 本地设置 `PUBLIC_BASE_URL=http://localhost:8080`、`COOKIE_SECURE=false`，保留示例中的 Mailpit 配置，执行：

   ```sh
   docker compose --env-file deploy/.env -f deploy/compose.yaml --profile dev-mail up -d
   ```

4. 访问 `http://localhost:8080`，开发邮件在 `http://localhost:8025` 查看。Mailpit 只捕获邮件，不向真实地址投递。`migrate` 等待数据库健康并执行迁移，只有迁移成功才启动 `app`。
5. 运行 `./deploy/smoke.sh`。这是服务与静态页面冒烟；完整对局和权限验收以 `docs/ACCEPTANCE.md` 中的实际结果为准。

生产环境必须使用 HTTPS 的 `PUBLIC_BASE_URL`、`COOKIE_SECURE=true` 和真实 SMTP。外部 SMTP 设置 `SMTP_STARTTLS=true`；不要把 Mailpit 当生产邮件服务。`SMTP_USER`、`SMTP_PASSWORD` 和 `SMTP_FROM` 按邮件服务配置，并验证真实收件箱能收到注册、重置邮件。SMTP 接受邮件不等于送达。

`deploy/Caddyfile.example` 展示同主机 Caddy 终止 TLS；应用仅绑定主机回环地址 `127.0.0.1:8080`。若反向代理也在容器中，应在专用网络通过 `app:8080` 连接，不把示例的回环地址误用于另一容器。代理必须透传 WebSocket Upgrade，不能缓存鉴权/API 响应。应用域名来源校验使用 `PUBLIC_BASE_URL`。

直接连接时保持 `TRUSTED_PROXY_CIDRS` 为空；经反向代理时，将应用实际看到的代理源 IP 配置为精确 `/32`（IPv6 为 `/128`），多个值用逗号分隔。Docker 的主机代理连接可能显示为桥接网关，需按实际网络核对，不能猜测。可信入口必须正确覆盖或追加 `X-Forwarded-For`；应用从右向左越过已配置的可信代理。不要信任所有地址或整个不受控私网。否则会让客户端伪造 IP 逃避限流；完全不配置代理则所有入口用户共用代理 IP 限额。

运行配置更新后重建容器，无需重新构建 React。妥善保存邮件加密和令牌签名密钥；随意更换会影响尚未完成的验证链接、邮件任务或 Bot 会话。

## GitHub Actions 和 Docker Hub

仓库已配置的 **Environment `DOCKERHUB`** 中，`USER` 是 Docker Hub 账号、`TOKEN` 是具有目标仓库 push 权限的访问 token。只有发布和手动晋升 job 绑定该 Environment。无需新增必填 secret，默认镜像是 `docker.io/<USER>/openmajiang`；可选 Environment variable `DOCKERHUB_NAMESPACE` 用于有授权的组织命名空间。

`.github/workflows/ci.yml` 的行为：

| 触发 | 验证 | 输出 |
| --- | --- | --- |
| PR | 原生 amd64/arm64 Go CGO、race、前端、真实 PostgreSQL、迁移重入、原生镜像与 HTTP 冒烟、Chromium账号/对局/观战/重连 E2E | 不登录、不推送、不使用环境密钥 |
| main push | 同一套验证全部成功 | `sha-<完整提交 SHA>` 和可变 `main` |
| `v*` tag push | 同一套验证；校验 SemVer、main 祖先关系和该提交的 `VERSION` | 不可重复发布的精确版本，例如 `0.1.0` 或 `0.2.0-rc.1` |

两个架构都用原生 GitHub runner 跑测试，发布构建使用 Buildx/QEMU 生成 `linux/amd64,linux/arm64`。发布附带 SBOM 和 provenance。workflow 中第三方 Actions 固定为从官方仓库核对的完整提交 SHA。当前文件提供两架构验证路径，只有对应 CI 实际成功后才能宣称双架构已验证；本地没有 Docker 时不能据此宣称镜像冒烟通过。

发布不自动部署服务器，也不自动移动 `latest`。`promote.yml` 从 main 手动触发，填写已成功发布的稳定版本和该次构建的完整 digest。它核对 tag 可从 main 到达、版本文件、成功的 tag workflow、registry digest、两架构 OCI 源仓库/版本/提交元数据，再把同一 digest 晋升为 `latest`，不重新构建。预发布不能晋升。

示例发版流程：更新 `VERSION` 并合并通过测试的 PR，随后在 main 对应提交创建 `v0.1.0`。SemVer 不接受数字段前导零；Docker tag 不能直接表示 `+build` 元数据，因此当前发布入口明确不接受该形式。已有版本标签绝不覆盖；修正后的版本使用新的补丁版本。

为防止凭据被未审核代码取得，应在仓库设置中保护 main、限制 Environment `DOCKERHUB` 可部署分支/标签，并按团队需要开启 required reviewers。仓库代码不能替代 GitHub 的保护设置；这些设置不会被本 PR 隐式修改。

## 升级、备份与恢复

1. 先安排维护窗口，停止接收新对局并让已有对局完成。升级前记录当前镜像 digest、数据库版本和必要配置，执行 `make backup`。
2. 备份文件位于 `backups/*.dump`，使用 PostgreSQL custom 格式及 SHA256 校验，文件权限受 `umask 077` 保护。将备份复制到有访问控制的异机存储；本机卷不是备份。应用密钥不在 SQL 备份中，应另外安全保存。
3. 更新 `.env` 中的不可变镜像 digest，执行 `docker compose --env-file deploy/.env -f deploy/compose.yaml pull`，再执行相同 compose 的 `up -d`。迁移失败时应用不应继续启动。
4. 验证 `/health/ready`、注册邮件、登录、一桌真人/Bot 和匿名观战。只有弃牌牌面可以进入观战和公开回放。

还原会替换目标数据库，先在隔离环境演练，并确保备份与准备启动的镜像兼容：

```sh
RESTORE_CONFIRM=replace-openmajiang ./deploy/restore.sh backups/openmajiang-YYYYMMDDTHHMMSSZ.dump
```

脚本停止 app，验证备份清单并在单个事务中执行恢复。成功或失败后都不会擅自重启 app；检查结果、镜像 digest 与密钥后再启动。不能对新 schema 直接运行任意旧镜像；有不兼容迁移时，使用匹配的旧镜像与升级前备份一起恢复，会丢失备份之后的数据。不得删除数据库卷来处理启动错误。

Compose 使用最多 5 个 10 MB 的 JSON 日志文件控制磁盘增长，Mailpit 为 3 个；关闭应用有 90 秒宽限。日志不得包含完整数据库 URL、密码、邮件链接、session/Bot token、完整牌墙或其他玩家手牌。持续观察数据库容量、邮件积压、重连与动作超时；容器健康只说明进程/数据库可用，不代表所有牌局规则已验收。
