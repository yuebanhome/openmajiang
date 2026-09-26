import { useState } from "react";
import { api, errorMessage, listFrom } from "./api";
import {
  Badge,
  Button,
  Empty,
  FormField,
  Link,
  Loading,
  Modal,
  Notice,
  PageHeading,
} from "./components";
import { useResource } from "./hooks";
import type { Bot, MatchRecord, User } from "./types";
export function Bots({ user }: { user?: User }) {
  const r = useResource<unknown>(user ? "/v1/bots" : null, 5000);
  const [creating, setCreating] = useState(false);
  const [selected, setSelected] = useState<string>();
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [secret, setSecret] = useState("");
  const [busy, setBusy] = useState("");
  const bots = listFrom<Bot>(r.data, "bots");
  const bot = bots.find((b) => b.id === selected);
  const versions = useResource<unknown>(
    selected ? `/v1/bots/${selected}/versions` : null,
  );
  const credentials = useResource<unknown>(
    selected ? `/v1/bots/${selected}/credentials` : null,
  );
  async function run(
    action: string,
    path: string,
    payload: unknown = {},
    method = "POST",
  ) {
    setBusy(action);
    setError("");
    setMessage("");
    try {
      const data = await api<Record<string, unknown>>(path, {
        method,
        body: method === "DELETE" ? undefined : JSON.stringify(payload),
      });
      if (
        typeof data.api_key === "string" ||
        typeof data.token === "string" ||
        typeof data.key === "string"
      )
        setSecret(String(data.api_key ?? data.token ?? data.key));
      else setMessage("操作已保存。");
      r.reload();
      versions.reload();
      credentials.reload();
      return data;
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy("");
    }
  }
  if (!user)
    return (
      <>
        <PageHeading eyebrow="DEVELOPER CONSOLE" title="让你的 Bot 上桌。">
          你运行策略，我们负责规则、牌桌和公平裁决。
        </PageHeading>
        <div className="bot-intro">
          <span className="code-mark">{"{ }"}</span>
          <h2>从第一个合法动作开始</h2>
          <p>
            用 TypeScript 或 Python 连接自托管 Bot。
            <br />
            服务端提供本人视角和合法动作，你只需返回 option_id。
          </p>
          <Link className="button primary" href="/auth/login?return_to=%2Fbots">
            登录并创建 Bot
          </Link>
          <Link className="button secondary" href="/developers">
            阅读接入指南
          </Link>
        </div>
      </>
    );
  return (
    <>
      <PageHeading
        eyebrow="DEVELOPER CONSOLE"
        title="我的 Bot"
        action={
          <Button
            onClick={() => setCreating(true)}
            disabled={!user.email_verified}
          >
            ＋ 创建 Bot
          </Button>
        }
      >
        独立身份、版本和运行凭证。你的策略，由你运行。
      </PageHeading>
      <Notice error>{error || r.error}</Notice>
      <Notice>{message}</Notice>
      {!user.email_verified && (
        <Notice>
          验证邮箱后才能创建 Bot。<Link href="/settings">去验证 →</Link>
        </Notice>
      )}
      <div className="bot-layout">
        <section>
          {r.loading ? (
            <Loading />
          ) : bots.length ? (
            <div className="bot-cards">
              {bots.map((b) => (
                <button
                  key={b.id}
                  className={`bot-card ${selected === b.id ? "selected" : ""}`}
                  onClick={() => setSelected(b.id)}
                >
                  <div className="bot-avatar">AI</div>
                  <div>
                    <h3>{b.name}</h3>
                    <p>
                      {b.enabled === false
                        ? "已停用"
                        : `当前版本 ${b.current_version?.slice(0, 14) ?? "—"}`}
                    </p>
                  </div>
                  <Badge>{b.enabled === false ? "已停用" : "已启用"}</Badge>
                </button>
              ))}
            </div>
          ) : (
            <Empty
              title="还没有自己的 Bot"
              action={
                <Button
                  tone="secondary"
                  onClick={() => setCreating(true)}
                  disabled={!user.email_verified}
                >
                  创建第一个 Bot
                </Button>
              }
            >
              每个 Bot 都有独立身份，不与所有者账号共享运行凭据。
            </Empty>
          )}
          {bot && (
            <section className="panel bot-detail">
              <div className="section-heading">
                <h2>{bot.name}</h2>
                <span className="mono">{bot.id.slice(0, 12)}</span>
              </div>
              <BotRuntimePanel id={bot.id} />
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  const f = new FormData(e.currentTarget);
                  void run(
                    "profile",
                    `/v1/bots/${bot.id}`,
                    { name: f.get("name") },
                    "PATCH",
                  );
                }}
              >
                <FormField label="名称">
                  <input
                    name="name"
                    defaultValue={bot.name}
                    key={bot.id}
                    required
                    maxLength={40}
                  />
                </FormField>
                <div className="room-actions">
                  <Button
                    type="submit"
                    tone="secondary"
                    busy={busy === "profile"}
                  >
                    保存 Bot 资料
                  </Button>
                  <Button
                    type="button"
                    tone={bot.enabled === false ? "secondary" : "danger"}
                    onClick={() =>
                      run(
                        "enabled",
                        `/v1/bots/${bot.id}`,
                        { enabled: bot.enabled === false },
                        "PATCH",
                      )
                    }
                  >
                    {bot.enabled === false ? "重新启用" : "停用并撤销连接"}
                  </Button>
                </div>
              </form>
              <hr />
              <h3>不可变策略版本</h3>
              <form
                className="inline-form"
                onSubmit={(e) => {
                  e.preventDefault();
                  const f = new FormData(e.currentTarget);
                  void run("version", `/v1/bots/${bot.id}/versions`, {
                    label: f.get("version"),
                    metadata: {
                      rulesets: [{ id: "openmajiang.mcr", version: "1.0.0" }],
                      protocol_version: "1.0",
                      capabilities: [
                        "mcr.flowers",
                        "mcr.8-point-minimum",
                        "mcr.seat-rotation",
                      ],
                    },
                  });
                }}
              >
                <input
                  name="version"
                  placeholder="版本号，例如 0.1.0"
                  required
                  pattern="[A-Za-z0-9._-]+"
                  maxLength={40}
                />
                <Button
                  type="submit"
                  tone="secondary"
                  busy={busy === "version"}
                >
                  发布版本
                </Button>
              </form>
              <ul className="simple-list">
                {listFrom<{ id: string; label: string }>(
                  versions.data,
                  "versions",
                ).map((v) => (
                  <li key={v.id}>
                    {v.label}
                    <small className="mono">{v.id.slice(0, 12)}</small>
                  </li>
                ))}
              </ul>
              <hr />
              <div className="section-heading">
                <h3>运行凭证</h3>
                <Button
                  tone="secondary"
                  busy={busy === "key"}
                  onClick={() =>
                    run("key", `/v1/bots/${bot.id}/credentials`, {
                      name: "自托管连接",
                    })
                  }
                >
                  生成新 Key
                </Button>
              </div>
              <p className="caption">
                Key 只展示一次。将它放到你的运行环境中，不要写入代码仓库。
              </p>
              <ul className="simple-list">
                {listFrom<{
                  id: string;
                  revoked_at?: string;
                  created_at: string;
                }>(credentials.data, "credentials")
                  .filter((c) => !c.revoked_at)
                  .map((c) => (
                    <li key={c.id}>
                      <code>{c.id.slice(0, 18)}…</code>
                      <button
                        className="text-button danger-text"
                        onClick={() => {
                          if (
                            window.confirm(
                              "撤销此凭证并断开使用它的 Bot 连接？",
                            )
                          )
                            void run(
                              "revoke",
                              `/v1/bots/${bot.id}/credentials/${c.id}`,
                              {},
                              "DELETE",
                            );
                        }}
                      >
                        撤销
                      </button>
                    </li>
                  ))}
              </ul>
              <hr />
              <div className="section-heading">
                <h3>连续 Bot 匹配</h3>
                <Link
                  href={`/bots/${bot.id}/statistics`}
                  className="arrow-link"
                >
                  查看 Bot 统计 ↗
                </Link>
              </div>
              <p>
                公共匹配池不允许同一所有者的多个 Bot
                同桌。自测请创建邀请入座的自测房间。
              </p>
              <div className="room-actions">
                <Button
                  disabled={!bot.connected}
                  busy={busy === "queue"}
                  onClick={() =>
                    run("queue", `/v1/bots/${bot.id}/queue`, {
                      ruleset_id: "openmajiang.mcr",
                      ruleset_version: "1.0.0",
                      match_format: "standard_16",
                      continuous: true,
                    })
                  }
                >
                  加入公共队列
                </Button>
                <Button
                  tone="secondary"
                  busy={busy === "stop"}
                  onClick={() =>
                    run("stop", `/v1/bots/${bot.id}/queue`, {}, "DELETE")
                  }
                >
                  停止连续匹配
                </Button>
              </div>
            </section>
          )}
        </section>
        <aside className="panel bot-guide">
          <span className="eyebrow">THREE STEPS</span>
          <h3>接入很简单</h3>
          <ol>
            <li>
              <strong>创建身份和版本</strong>
              <p>声明你的 Bot 支持国标 1.0.0、花牌和 8 分门槛。</p>
            </li>
            <li>
              <strong>在自己的机器运行</strong>
              <p>用运行 Key 换取短期会话，再连接 Bot WebSocket。</p>
            </li>
            <li>
              <strong>接收观测，提交选项</strong>
              <p>
                按服务端 legal_actions 选择 option_id，收到 recorded
                后等待裁决。
              </p>
            </li>
          </ol>
          <Link href="/developers" className="arrow-link">
            完整协议与 SDK →
          </Link>
          <div className="mini-callout">
            运行 Key、玩家 Cookie 和观战票据三者互不替代。
          </div>
        </aside>
      </div>
      {creating && (
        <Modal title="创建一个 Bot" onClose={() => setCreating(false)}>
          <form
            onSubmit={async (e) => {
              e.preventDefault();
              const f = new FormData(e.currentTarget);
              const data = await run("create", "/v1/bots", {
                name: f.get("name"),
              });
              if (data) {
                setCreating(false);
                const b = data.bot as Bot | undefined;
                if (b) setSelected(b.id);
              }
            }}
          >
            <FormField label="Bot 名称">
              <input
                name="name"
                required
                minLength={2}
                maxLength={40}
                placeholder="给你的策略起个名字"
              />
            </FormField>
            <Button className="wide" type="submit" busy={busy === "create"}>
              创建 Bot
            </Button>
          </form>
        </Modal>
      )}
      {secret && (
        <Modal title="保存你的 Bot Key" onClose={() => setSecret("")}>
          <Notice>这是唯一一次展示完整凭证。关闭后无法再次查看。</Notice>
          <code className="secret">{secret}</code>
          <div className="room-actions">
            <Button
              onClick={async () => {
                try {
                  await navigator.clipboard.writeText(secret);
                  setMessage("Key 已复制，请妥善保存。");
                } catch {
                  setError("无法访问剪贴板，请手动复制。");
                }
              }}
            >
              复制 Key
            </Button>
            <Button tone="secondary" onClick={() => setSecret("")}>
              已保存，关闭
            </Button>
          </div>
        </Modal>
      )}
    </>
  );
}

function BotRuntimePanel({ id }: { id: string }) {
  const runtime = useResource<{
    online: boolean;
    connected: boolean;
    presence: string;
    active_room_id: string;
    active_match_id: string;
    queued: boolean;
    continuous: boolean;
    last_error: string;
    last_error_at?: string;
    recent_errors: { code: string; at: string; match_id?: string }[];
  }>(`/v1/bots/${encodeURIComponent(id)}/status`, 5000);
  const history = useResource<unknown>(
    `/v1/me/matches?bot_id=${encodeURIComponent(id)}&limit=5`,
    15000,
  );
  const state = runtime.data;
  return (
    <div className="bot-runtime">
      <Notice error>{runtime.error || history.error}</Notice>
      <dl className="details">
        <div>
          <dt>运行状态</dt>
          <dd>
            {state?.connected
              ? "WebSocket 已连接"
              : state?.online
                ? "Runner 在线，等待分桌"
                : "离线"}
          </dd>
        </div>
        <div>
          <dt>匹配队列</dt>
          <dd>
            {state?.queued
              ? state.continuous
                ? "持续匹配中"
                : "已在队列"
              : "未排队"}
          </dd>
        </div>
        <div>
          <dt>当前牌桌</dt>
          <dd>
            {state?.active_room_id ? (
              <Link
                href={`/rooms/${state.active_room_id}`}
                className="arrow-link"
              >
                打开房间 ↗
              </Link>
            ) : (
              "尚未入座"
            )}
          </dd>
        </div>
      </dl>
      {state?.active_match_id && (
        <div className="room-actions">
          <Link
            href={`/watch/${state.active_room_id}`}
            className="button secondary"
          >
            观看公开弃牌
          </Link>
          <Link
            href={`/replays/${state.active_match_id}?bot_id=${encodeURIComponent(id)}`}
            className="button secondary"
          >
            该 Bot 私有复盘
          </Link>
        </div>
      )}
      <details className="bot-errors">
        <summary>
          近期运行错误{" "}
          {state?.recent_errors?.length
            ? `(${state.recent_errors.length})`
            : ""}
        </summary>
        {state?.recent_errors?.length ? (
          <ul className="simple-list">
            {state.recent_errors.map((e, i) => (
              <li key={`${e.at}-${i}`}>
                <code>{e.code}</code>
                <small>{new Date(e.at).toLocaleString("zh-CN")}</small>
                {e.match_id && (
                  <Link
                    href={`/replays/${e.match_id}?bot_id=${encodeURIComponent(id)}`}
                  >
                    对应复盘 ↗
                  </Link>
                )}
              </li>
            ))}
          </ul>
        ) : (
          <p className="caption">
            暂无已记录的错误；这不代表策略已经完成所有接入测试。
          </p>
        )}
      </details>
      <div className="section-heading">
        <h3>最近对局</h3>
        <Link
          href={`/history?bot_id=${encodeURIComponent(id)}`}
          className="arrow-link"
        >
          全部 Bot 对局 ↗
        </Link>
      </div>
      <ul className="simple-list">
        {listFrom<MatchRecord>(history.data, "matches").map((m) => (
          <li key={m.id}>
            <Link href={`/replays/${m.id}?bot_id=${encodeURIComponent(id)}`}>
              本人 Bot 复盘 · {m.id.slice(0, 12)}
            </Link>
            <small>
              {m.created_at
                ? new Date(m.created_at).toLocaleDateString("zh-CN")
                : ""}
            </small>
          </li>
        ))}
      </ul>
      {!history.loading &&
        !listFrom<MatchRecord>(history.data, "matches").length && (
          <p className="caption">尚无对局记录。</p>
        )}
      <hr />
    </div>
  );
}
