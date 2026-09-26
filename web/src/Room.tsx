import { useState } from "react";
import { errorMessage, listFrom, post } from "./api";
import {
  Badge,
  Button,
  Empty,
  FormField,
  Link,
  Loading,
  Notice,
  PageHeading,
  formatName,
  modeName,
  statusName,
} from "./components";
import { navigate, useResource } from "./hooks";
import type { Bot, Room, User } from "./types";
export function RoomPage({ id, user }: { id: string; user?: User }) {
  const r = useResource<{ room: Room }>(
    `/v1/public/rooms/${encodeURIComponent(id)}`,
    2500,
  );
  const privateRoom = useResource<{
    room: Room;
    own_participant_id?: string;
    invite_code?: string;
  }>(user ? `/v1/rooms/${encodeURIComponent(id)}` : null, 2500);
  const botsResource = useResource<unknown>(user ? "/v1/bots" : null);
  const room = privateRoom.data?.room ?? r.data?.room;
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [code, setCode] = useState(
    new URLSearchParams(location.search).get("code") ?? "",
  );
  const [bot, setBot] = useState("builtin:basic_heuristic");
  if (!room)
    return (
      <>
        <PageHeading title="牌桌等待区" />
        <Notice error>{r.error}</Notice>
        {r.loading ? (
          <Loading />
        ) : (
          <Empty
            title="这张牌桌暂时无法打开"
            action={<Link href="/">回大厅</Link>}
          >
            请确认房间链接，或稍后重试。
          </Empty>
        )}
      </>
    );
  const seat = room.seats?.find(
    (s) => s.participant_id === privateRoom.data?.own_participant_id,
  );
  const owner = room.owner_id === user?.id;
  const active = ["playing", "running", "active"].includes(room.status);
  const waiting = room.status === "waiting";
  async function action(name: string, payload: unknown = {}) {
    if (!user)
      return navigate(
        `/auth/login?return_to=${encodeURIComponent(`/rooms/${id}`)}`,
      );
    if (!user.email_verified) return navigate("/settings");
    setBusy(name);
    setError("");
    setMessage("");
    try {
      const data = await post<{
        room?: Room;
        match_id?: string;
        invite_code?: string;
      }>(`/v1/rooms/${encodeURIComponent(id)}/${name}`, payload);
      if (name === "rematch" && data.room)
        navigate(
          `/rooms/${data.room.id}${data.invite_code ? `?code=${encodeURIComponent(data.invite_code)}` : ""}`,
        );
      else if (name === "leave" || name === "close") navigate("/");
      else if (name === "start")
        navigate(`/${room?.mode === "bot_only" ? "watch" : "play"}/${id}`);
      else {
        setMessage(name === "ready" ? "准备状态已更新。" : "房间已更新。");
        r.reload();
        privateRoom.reload();
        if (data.room?.match_id && name === "join") navigate(`/play/${id}`);
      }
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy("");
    }
  }
  return (
    <>
      <PageHeading
        eyebrow="THE WAITING ROOM"
        title={room.name}
        action={
          <Link href={`/watch/${id}`} className="button secondary">
            观战弃牌 ↗
          </Link>
        }
      >
        {modeName[room.mode]} · {formatName[room.match_format]} · 国标{" "}
        {room.ruleset_version}
      </PageHeading>
      <Notice error>{error}</Notice>
      <Notice>{message}</Notice>
      <div className="room-layout">
        <section className="panel">
          <div className="section-heading">
            <h2>入座与准备</h2>
            <Badge live={active}>
              {statusName[room.status] ?? room.status}
            </Badge>
          </div>
          <div className="waiting-seats">
            {Array.from({ length: room.capacity ?? 4 }, (_, i) => {
              const s = room.seats?.find((s) => s.seat_id === i);
              return (
                <div className={`waiting-seat ${s ? "occupied" : ""}`} key={i}>
                  <span className="seat-wind">
                    {["東", "南", "西", "北"][i] ?? i + 1}
                  </span>
                  <div className="avatar">
                    {s
                      ? s.kind === "bot" || s.bot_id
                        ? "AI"
                        : (s.name ?? s.display_name ?? "人").slice(0, 1)
                      : "+"}
                  </div>
                  <h3>{s?.name ?? s?.display_name ?? "等待牌友"}</h3>
                  <span className="muted">
                    {s ? (s.ready ? "已准备" : "尚未准备") : "空座位"}
                  </span>
                  {s?.participant_id === privateRoom.data?.own_participant_id &&
                    !!s && <Badge>你</Badge>}
                </div>
              );
            })}
          </div>
          <div className="room-actions">
            {!active && !waiting ? (
              <>
                {owner && (
                  <Button
                    busy={busy === "rematch"}
                    onClick={() => action("rematch")}
                  >
                    再来一场
                  </Button>
                )}
                {room.match_id && (
                  <Link
                    href={`/matches/${room.match_id}`}
                    className="button secondary"
                  >
                    查看公开牌谱
                  </Link>
                )}
              </>
            ) : active ? (
              seat ? (
                <Link href={`/play/${id}`} className="button primary">
                  回到我的牌桌 →
                </Link>
              ) : (
                <Link href={`/watch/${id}`} className="button primary">
                  观看这场对局 →
                </Link>
              )
            ) : seat ? (
              <>
                <Button
                  busy={busy === "ready"}
                  onClick={() => action("ready", { ready: !seat.ready })}
                  tone={seat.ready ? "secondary" : "primary"}
                >
                  {seat.ready ? "取消准备" : "我准备好了"}
                </Button>
                <Button
                  tone="quiet"
                  busy={busy === "leave"}
                  onClick={() => action("leave")}
                >
                  离开房间
                </Button>
              </>
            ) : room.mode !== "bot_only" ? (
              <Button
                busy={busy === "join"}
                onClick={() => action("join", { invite_code: code })}
              >
                {user ? "加入这张牌桌" : "登录并入座"}
              </Button>
            ) : null}
            {owner && waiting && (
              <>
                <Button
                  tone="quiet"
                  busy={busy === "close"}
                  onClick={() => action("close")}
                >
                  关闭房间
                </Button>
                <Button
                  busy={busy === "start"}
                  disabled={
                    room.seats.length !== room.capacity ||
                    !room.seats.every((s) => s.ready)
                  }
                  onClick={() => action("start")}
                >
                  开始对局
                </Button>
              </>
            )}
          </div>
          {waiting && (
            <p className="caption centered">
              所有座位到齐并准备后，由房主开始。观战不会占用座位。
            </p>
          )}
        </section>
        <aside className="stack">
          <section className="panel">
            <h3>房间信息</h3>
            <dl className="details">
              <div>
                <dt>房间号</dt>
                <dd className="mono">{room.id.slice(0, 12)}</dd>
              </div>
              <div>
                <dt>赛程</dt>
                <dd>{formatName[room.match_format]}</dd>
              </div>
              <div>
                <dt>观战</dt>
                <dd>所有人可看弃牌</dd>
              </div>
            </dl>
            {code && owner && (
              <div className="invite-box">
                <small>入座邀请码</small>
                <code>{code}</code>
                <Button
                  tone="quiet"
                  onClick={async () => {
                    try {
                      await navigator.clipboard.writeText(
                        `${location.origin}/rooms/${id}?code=${encodeURIComponent(code)}`,
                      );
                      setMessage("邀请链接已复制。");
                    } catch {
                      setMessage("请手动复制此页地址和邀请码。");
                    }
                  }}
                >
                  复制邀请链接
                </Button>
              </div>
            )}
            {!seat && (
              <FormField label="邀请码（邀请桌必填）">
                <input
                  value={code}
                  onChange={(e) => setCode(e.target.value)}
                  autoComplete="off"
                />
              </FormField>
            )}
            {owner &&
              (room as Room & { invite_only?: boolean }).invite_only && (
                <Button
                  tone="quiet"
                  onClick={async () => {
                    try {
                      const result = await post<{ invite_code: string }>(
                        `/v1/rooms/${id}/invite-rotate`,
                      );
                      setCode(result.invite_code);
                      setMessage("已生成新邀请码，旧邀请失效。");
                    } catch (err) {
                      setError(errorMessage(err));
                    }
                  }}
                >
                  重新生成邀请码
                </Button>
              )}
            <Link href="/rules" className="arrow-link">
              查看规则与计时说明 ↗
            </Link>
          </section>
          {owner && waiting && room.mode !== "human_only" && (
            <section className="panel">
              <h3>添加一位 Bot</h3>
              <FormField label="选择对手">
                <select value={bot} onChange={(e) => setBot(e.target.value)}>
                  <option value="builtin:basic_heuristic">
                    内置 · 基础策略
                  </option>
                  <option value="builtin:random_legal">内置 · 随机合法</option>
                  {listFrom<Bot>(botsResource.data, "bots").map((b) => (
                    <option value={b.id} key={b.id} disabled={!b.online}>
                      {b.name}
                      {b.online ? "" : "（离线）"}
                    </option>
                  ))}
                </select>
              </FormField>
              <Button
                tone="secondary"
                busy={busy === "bots"}
                disabled={room.seats.length >= (room.capacity ?? 4)}
                onClick={() =>
                  action(
                    "bots",
                    bot.startsWith("builtin:")
                      ? { builtin: bot.slice(8) }
                      : { bot_id: bot },
                  )
                }
              >
                添加到空位
              </Button>
            </section>
          )}
        </aside>
      </div>
    </>
  );
}
export function JoinPage({ user }: { user?: User }) {
  const [code, setCode] = useState(
    new URLSearchParams(location.search).get("code") ?? "",
  );
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <div className="narrow">
      <PageHeading title="通过邀请入座" />
      <Notice error>{error}</Notice>
      <form
        className="panel"
        onSubmit={async (e) => {
          e.preventDefault();
          if (!user)
            return navigate(
              `/auth/login?return_to=${encodeURIComponent(location.pathname + location.search)}`,
            );
          setBusy(true);
          try {
            const r = await post<{ room: Room }>("/v1/rooms/join", {
              invite_code: code,
            });
            navigate(`/rooms/${r.room.id}`);
          } catch (err) {
            setError(errorMessage(err));
          } finally {
            setBusy(false);
          }
        }}
      >
        <FormField label="邀请码">
          <input
            value={code}
            onChange={(e) => setCode(e.target.value)}
            required
          />
        </FormField>
        <Button type="submit" busy={busy}>
          加入房间
        </Button>
      </form>
    </div>
  );
}
