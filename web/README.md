# OpenMajiang 前端

React / TypeScript / Vite，生产资源由同版本 Go 服务提供，所有 API 和 WebSocket 使用同域 `/v1`。开发时 `npm ci && npm run dev`，Vite 代理到 `127.0.0.1:8080`。

- `npm run typecheck`：类型契约检查。
- `npm test`：身份边界、共享协议 fixture、合法动作、幂等确认、控制权、弃牌投影和 DOM 测试。
- `npm run build`：产物到 `dist/`，供 Go embed。
- `npm run test:e2e`：真实服务和邮件链路的 Playwright 测试，分别运行 Chromium 桌面、Chromium 移动视口及 WebKit 移动视口；WebKit 是引擎模拟，不代表真机 Safari 验证。需要 Go / PostgreSQL / Mailpit 已启动，默认服务 `http://127.0.0.1:8080`、Mailpit API `http://127.0.0.1:8025`；分别通过 `PUBLIC_BASE_URL`、`MAILPIT_HTTP_URL` 覆盖。先执行 `npx playwright install --with-deps chromium webkit`。

公开视图由 `spectator.ts` 重新构造，唯一允许的牌面来源为公开弃牌的 `kind`。不复用参赛者视图，也不展示副露、补花、和牌拆分或结束后的手牌；公开回放采用同一投影。参赛动作需要匹配的观测引用、当前控制凭证与 CSRF。控制凭证仅存在当前标签页内存；第二标签页需明确接管。

WebSocket 正常时直接消费快照和匹配的决策，只有连接中断时用 REST 拉取恢复。收到 `recorded` 不删除手牌、不乐观修改分数。退出、会话过期及跨标签退出会销毁私有视图。

字体通过锁定的 `@fontsource-variable/noto-sans-sc` 自托管，浏览器不访问第三方字体服务。字体许可随产物保存在 `FONT-LICENSE.txt`。
