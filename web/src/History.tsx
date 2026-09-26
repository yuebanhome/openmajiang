import { useEffect, useRef, useState } from "react";
import { api, APIError, errorMessage, listFrom } from "./api";
import {
  Badge,
  Button,
  Empty,
  FormField,
  Link,
  Loading,
  Notice,
  PageHeading,
  TileFace,
  formatName,
  modeName,
  statusName,
} from "./components";
import { useResource } from "./hooks";
import { useCursorList } from "./pagination";
import { normalizeSpectator } from "./spectator";
import { normalizeParticipant } from "./table-state";
import { PrivateSettlement, SpectatorTable } from "./Table";
import type {
  Bot,
  MatchRecord,
  ParticipantView,
  SpectatorView,
  User,
} from "./types";
export type ArchiveSummary = {
  version: string;
  completed_hands: number;
  scores: {
    participant_id: string;
    kind: string;
    total: number;
    rank?: number;
    standard_points?: { numerator: number; denominator: number };
  }[];
};
export function ArchiveResult({ summary }: { summary?: ArchiveSummary }) {
  return (
    <section className="panel archive-summary">
      <h2>详细牌谱已归档</h2>
      <p>
        动作与牌面记录已超过保留期限，不能继续回放。必要的累计得分与比赛结果仍然保留。
      </p>
      {summary && (
        <>
          <p>已完成 {summary.completed_hands} 盘</p>
          <div className="audit-scroll">
            <table>
              <thead>
                <tr>
                  <th>参赛身份</th>
                  <th>类型</th>
                  <th>累计比赛分</th>
                  <th>名次</th>
                  <th>标准分</th>
                </tr>
              </thead>
              <tbody>
                {summary.scores.map((s) => (
                  <tr key={s.participant_id}>
                    <td className="mono">{s.participant_id}</td>
                    <td>{s.kind === "human" ? "真人" : "Bot"}</td>
                    <td>{s.total}</td>
                    <td>{s.rank ?? "—"}</td>
                    <td>
                      {s.standard_points && s.standard_points.denominator > 0
                        ? s.standard_points.numerator /
                          s.standard_points.denominator
                        : "—"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </section>
  );
}
export function History({ user }: { user?: User }) {
  const initialBot = new URLSearchParams(location.search).get("bot_id") ?? "";
  const [subject, setSubject] = useState(
    initialBot ? `bot:${initialBot}` : "public",
  );
  const [filters, setFilters] = useState<Record<string, string>>({});
  const bots = useResource<unknown>(user ? "/v1/bots" : null);
  const botID = subject.startsWith("bot:") ? subject.slice(4) : "";
  const mine = subject !== "public";
  const query = new URLSearchParams({ limit: "25", ...filters });
  if (botID) query.set("bot_id", botID);
  const path =
    mine && !user ? null : `${mine ? "/v1/me" : "/v1/public"}/matches?${query}`;
  const r = useCursorList<
    MatchRecord & { archived_at?: string; public_summary?: ArchiveSummary }
  >(path);
  return (
    <>
      <PageHeading
        eyebrow="EVERY HAND TELLS A STORY"
        title="对局记录"
        action={
          <Link href="/statistics" className="button secondary">
            对局统计 ↗
          </Link>
        }
      >
        按参与身份、模式、规则与赛程寻找对局；公开牌谱始终只展示弃牌。
      </PageHeading>
      <div className="history-subject">
        <div className="tabs history-tabs">
          <button
            className={subject === "public" ? "active" : ""}
            onClick={() => setSubject("public")}
          >
            公开对局
          </button>
          <button
            className={subject === "me" ? "active" : ""}
            disabled={!user}
            onClick={() => setSubject("me")}
          >
            我的真人对局
          </button>
        </div>
        {user && (
          <select
            aria-label="选择自己的 Bot 对局"
            value={botID}
            onChange={(e) =>
              setSubject(e.target.value ? `bot:${e.target.value}` : "me")
            }
          >
            <option value="">选择我的 Bot…</option>
            {listFrom<Bot>(bots.data, "bots").map((b) => (
              <option key={b.id} value={b.id}>
                {b.name}
              </option>
            ))}
          </select>
        )}
      </div>
      <form
        className="history-filters panel"
        onSubmit={(e) => {
          e.preventDefault();
          const f = new FormData(e.currentTarget);
          setFilters(
            Object.fromEntries(
              [...f.entries()]
                .map(([k, v]) => [k, String(v).trim()])
                .filter(([, v]) => v !== ""),
            ),
          );
        }}
      >
        <FormField label="模式">
          <select name="mode">
            <option value="">全部模式</option>
            <option value="human_only">真人</option>
            <option value="mixed">混合</option>
            <option value="bot_only">Bot</option>
          </select>
        </FormField>
        <FormField label="规则">
          <select name="ruleset_id">
            <option value="">全部规则</option>
            <option value="openmajiang.mcr">国标麻将</option>
          </select>
        </FormField>
        <FormField label="规则版本">
          <input
            name="ruleset_version"
            placeholder="例如 1.0.0"
            maxLength={40}
          />
        </FormField>
        <FormField label="赛程">
          <select name="match_format">
            <option value="">全部赛程</option>
            <option value="standard_16">标准 16 盘</option>
            <option value="practice_4">四盘练习</option>
            <option value="practice_1">单盘练习</option>
          </select>
        </FormField>
        <FormField label="结果">
          <select name="status">
            <option value="">全部状态</option>
            <option value="active">进行中</option>
            <option value="completed">完整完成</option>
            <option value="ended_early">提前结束</option>
            <option value="aborted_by_server">平台中断</option>
            <option value="missing_ruleset">规则不可用</option>
            <option value="invalid_state">状态校验中断</option>
          </select>
        </FormField>
        <Button type="submit" tone="secondary">
          筛选
        </Button>
      </form>
      <Notice error>{r.error}</Notice>
      {mine && !user ? (
        <Notice>
          请先<Link href="/auth/login?return_to=%2Fhistory">登录</Link>
          后查看自己的对局。
        </Notice>
      ) : r.loading && !r.items.length ? (
        <Loading text="读取对局记录…" />
      ) : r.items.length ? (
        <div className="history-list">
          {r.items.map((m) => (
            <Link
              href={`/${mine ? "replays" : "matches"}/${m.id}${botID ? `?bot_id=${encodeURIComponent(botID)}` : ""}`}
              className="history-card"
              key={m.id}
            >
              <div className="history-symbol">牌</div>
              <div>
                <h3>{m.room_name || `对局 ${m.id.slice(0, 12)}`}</h3>
                <p>
                  {modeName[m.mode] ?? m.mode ?? "对局"} ·{" "}
                  {formatName[m.match_format] ?? m.match_format}
                  {m.ruleset_version ? ` · 国标 ${m.ruleset_version}` : ""}
                </p>
                {m.archived_at && <small>详细牌谱已归档 · 保留结算摘要</small>}
              </div>
              <div>
                <Badge>{statusName[m.status] ?? m.status}</Badge>
                <small>
                  {m.created_at
                    ? new Date(m.created_at).toLocaleString("zh-CN")
                    : ""}
                </small>
              </div>
              <span>↗</span>
            </Link>
          ))}
        </div>
      ) : (
        <Empty
          title="没有符合条件的对局"
          action={
            <Link href="/" className="button secondary">
              去大厅看看
            </Link>
          }
        >
          尝试调整筛选条件，或完成第一场比赛。
        </Empty>
      )}
      {r.next && (
        <div className="room-actions">
          <Button tone="secondary" busy={r.loading} onClick={r.more}>
            加载更早的对局
          </Button>
        </div>
      )}
    </>
  );
}
export function Replay({
  id,
  privateView,
  user,
  initialBotID = "",
}: {
  id: string;
  privateView: boolean;
  user?: User;
  initialBotID?: string;
}) {
  const generation = useRef(0);
  const [frames, setFrames] = useState<(SpectatorView | ParticipantView)[]>([]);
  const [index, setIndex] = useState(0);
  const [hand, setHand] = useState(1);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [playing, setPlaying] = useState(false);
  const [hasMore, setHasMore] = useState(false);
  const [cursor, setCursor] = useState(0);
  const [botID, setBotID] = useState(initialBotID);
  const [gone, setGone] = useState(false);
  const bots = useResource<unknown>(privateView && user ? "/v1/bots" : null);
  const metadata = useResource<{
    match: MatchRecord;
    archived?: boolean;
    archived_at?: string;
    summary?: ArchiveSummary;
  }>(`/v1/public/matches/${encodeURIComponent(id)}`);
  const match = metadata.data?.match;
  const archived = gone || metadata.data?.archived === true;
  const endpoint = `${privateView ? "/v1/me" : "/v1/public"}/matches/${encodeURIComponent(id)}/replay?hand_index=${hand}&limit=200${privateView && botID ? `&bot_id=${encodeURIComponent(botID)}` : ""}`;
  useEffect(() => {
    const version = ++generation.current;
    let active = true;
    const abort = new AbortController();
    setFrames([]);
    setIndex(0);
    setCursor(0);
    setLoading(true);
    setError("");
    setPlaying(false);
    setHasMore(false);
    if ((privateView && !user) || metadata.data?.archived) {
      setLoading(false);
      return () => abort.abort();
    }
    void api<Record<string, unknown>>(`${endpoint}&after=0`, {
      signal: abort.signal,
    })
      .then((data) => {
        if (!active || version !== generation.current) return;
        const raw = listFrom<Record<string, unknown>>(data, "frames");
        setFrames(
          raw.map((f) =>
            privateView ? normalizeParticipant(f) : normalizeSpectator(f),
          ),
        );
        setHasMore(raw.length === 200);
        setCursor(typeof data.next_after === "number" ? data.next_after : 0);
      })
      .catch((e) => {
        if (active) {
          if (e instanceof APIError && e.status === 410) {
            setGone(true);
            setHasMore(false);
          } else if (!(e instanceof DOMException && e.name === "AbortError"))
            setError(errorMessage(e));
        }
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      generation.current++;
      active = false;
      abort.abort();
    };
  }, [endpoint, privateView, user?.id, metadata.data?.archived]);
  useEffect(() => {
    if (!playing) return;
    const t = setInterval(
      () =>
        setIndex((i) => {
          if (i >= frames.length - 1) {
            setPlaying(false);
            return i;
          }
          return i + 1;
        }),
      900,
    );
    return () => clearInterval(t);
  }, [playing, frames.length]);
  async function more() {
    if (archived) return;
    setLoading(true);
    const requestedEndpoint = endpoint;
    const version = generation.current;
    try {
      const data = await api<Record<string, unknown>>(
        `${requestedEndpoint}&after=${cursor}`,
      );
      if (version !== generation.current) return;
      const raw = listFrom<Record<string, unknown>>(data, "frames");
      const normalized = raw.map((f) =>
        privateView ? normalizeParticipant(f) : normalizeSpectator(f),
      );
      setFrames((f) => [...f, ...normalized]);
      setCursor(typeof data.next_after === "number" ? data.next_after : cursor);
      setHasMore(raw.length === 200);
    } catch (e) {
      if (version !== generation.current) return;
      if (e instanceof APIError && e.status === 410) {
        setGone(true);
        setFrames([]);
        setPlaying(false);
        setHasMore(false);
      } else setError(errorMessage(e));
    } finally {
      if (version === generation.current) setLoading(false);
    }
  }
  const current = frames[index];
  const totalHands =
    match?.match_format === "standard_16"
      ? 16
      : match?.match_format === "practice_4"
        ? 4
        : 1;
  return (
    <>
      <PageHeading
        eyebrow={privateView ? "MY REPLAY" : "PUBLIC DISCARD REPLAY"}
        title={privateView ? "本人视角复盘" : "公开弃牌牌谱"}
        action={
          <Link
            href={`/history${botID ? `?bot_id=${encodeURIComponent(botID)}` : ""}`}
            className="button secondary"
          >
            全部对局
          </Link>
        }
      >
        {privateView
          ? "只读取本人或本人 Bot 获准查看的历史视角，每次切换都重新验证参赛身份。"
          : "公开牌谱仅显示弃牌，结算后也不会揭露任何玩家的手牌。"}
      </PageHeading>
      <Notice error>{error || metadata.error}</Notice>
      {privateView && user && !archived && (
        <FormField label="复盘身份">
          <select
            value={botID}
            onChange={(e) => {
              setFrames([]);
              setPlaying(false);
              setBotID(e.target.value);
            }}
          >
            <option value="">我的真人身份</option>
            {listFrom<Bot>(bots.data, "bots").map((b) => (
              <option key={b.id} value={b.id}>
                {b.name}（我的 Bot）
              </option>
            ))}
          </select>
        </FormField>
      )}
      {archived ? (
        <ArchiveResult summary={metadata.data?.summary} />
      ) : privateView && !user ? (
        <Notice>
          请先
          <Link
            href={`/auth/login?return_to=${encodeURIComponent(`/replays/${id}${botID ? `?bot_id=${encodeURIComponent(botID)}` : ""}`)}`}
          >
            登录参赛账号
          </Link>
          。
        </Notice>
      ) : (
        <>
          <div className="replay-controls panel">
            <label>
              第{" "}
              <select
                aria-label="选择盘数"
                value={hand}
                onChange={(e) => {
                  setFrames([]);
                  setPlaying(false);
                  setHand(Number(e.target.value));
                }}
              >
                {Array.from({ length: totalHands }, (_, i) => (
                  <option key={i + 1} value={i + 1}>
                    {i + 1}
                  </option>
                ))}
              </select>{" "}
              盘
            </label>
            <Button
              tone="secondary"
              disabled={!frames.length}
              onClick={() => setPlaying((p) => !p)}
            >
              {playing ? "暂停" : "播放"}
            </Button>
            <Button
              tone="quiet"
              disabled={index <= 0}
              onClick={() => {
                setPlaying(false);
                setIndex((i) => i - 1);
              }}
            >
              上一步
            </Button>
            <input
              type="range"
              aria-label="复盘进度"
              min={0}
              max={Math.max(0, frames.length - 1)}
              value={index}
              onChange={(e) => {
                setPlaying(false);
                setIndex(Number(e.target.value));
              }}
            />
            <Button
              tone="quiet"
              disabled={index >= frames.length - 1}
              onClick={() => {
                setPlaying(false);
                setIndex((i) => i + 1);
              }}
            >
              下一步
            </Button>
            <span>
              {frames.length ? index + 1 : 0} / {frames.length}
            </span>
          </div>
          {current ? (
            <>
              <SpectatorTable view={current} />
              {privateView && (
                <section className="hand-panel">
                  <h3>这一步的本人手牌</h3>
                  <div className="hand-tiles">
                    {(current as ParticipantView).hand.map((t) => (
                      <TileFace kind={t.kind} key={t.id} />
                    ))}
                  </div>
                  {(current as ParticipantView).outcome && (
                    <PrivateSettlement
                      outcome={(current as ParticipantView).outcome!}
                    />
                  )}
                </section>
              )}
            </>
          ) : loading ? (
            <Loading text="加载这一盘的记录…" />
          ) : (
            <Empty title="这盘暂时没有可用牌谱">
              可以选择其他盘数，或在比赛结束后重新查看。
            </Empty>
          )}
          {hasMore && (
            <Button onClick={more} busy={loading}>
              加载后续记录
            </Button>
          )}
        </>
      )}
    </>
  );
}
