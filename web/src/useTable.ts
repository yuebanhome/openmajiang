import { useCallback, useEffect, useRef, useState } from "react";
import { api, APIError, errorMessage, post } from "./api";
import { normalizeSpectator } from "./spectator";
import { normalizeParticipant } from "./table-state";
import { terminalState } from "./presentation";
import { attachDecision } from "./stream";
import { IncompatibleViewError } from "./compatibility";
import type { WireFrame } from "./stream";
import type { ParticipantView, SpectatorView } from "./types";
export function useTable(id: string, spectator: boolean) {
  const [view, setView] = useState<SpectatorView | ParticipantView>();
  const [error, setError] = useState("");
  const [connected, setConnected] = useState(false);
  const [archived, setArchived] = useState(false);
  const [incompatible, setIncompatible] = useState(false);
  const [nonce, setNonce] = useState(0);
  const [controlStatus, setControlStatus] = useState<
    "connecting" | "readonly" | "active"
  >("connecting");
  const token = useRef("");
  const resumePending = useRef(false);
  const socketRef = useRef<WebSocket | undefined>(undefined);
  const refresh = useCallback(() => setNonce((n) => n + 1), []);
  const takeControl = useCallback(async () => {
    const result = await post<{ control_token: string }>(
      `/v1/rooms/${encodeURIComponent(id)}/take-control`,
    );
    token.current = result.control_token;
    resumePending.current = true;
    setControlStatus("connecting");
    if (socketRef.current?.readyState === WebSocket.OPEN)
      socketRef.current.send(
        JSON.stringify({
          type: "resume_control",
          control_token: token.current,
        }),
      );
    else setNonce((n) => n + 1);
  }, [id]);
  const actionHeaders = () => ({ "X-Control-Token": token.current });
  useEffect(() => {
    let active = true;
    let socket: WebSocket | undefined;
    let abort: AbortController | undefined;
    let retry: ReturnType<typeof setTimeout> | undefined;
    let retries = 0;
    let loading = false;
    let socketSnapshot = false;
    let privateSnapshot: WireFrame | undefined;
    let lastStream = "";
    let lastSeq = 0;
    let finished = false;
    let gone = false;
    setView(undefined);
    setError("");
    setConnected(false);
    setArchived(false);
    setIncompatible(false);
    setControlStatus("connecting");
    const decode = (data: unknown) =>
      spectator ? normalizeSpectator(data) : normalizeParticipant(data);
    const rejectIncompatible = (error: IncompatibleViewError) => {
      gone = true;
      finished = true;
      privateSnapshot = undefined;
      token.current = "";
      setView(undefined);
      setIncompatible(true);
      setError(error.message);
      setControlStatus("readonly");
      if (retry) clearTimeout(retry);
      socket?.close();
    };
    const load = async () => {
      if (loading || socketSnapshot || gone) return;
      loading = true;
      abort = new AbortController();
      try {
        const data = await api<unknown>(
          spectator
            ? `/v1/public/rooms/${encodeURIComponent(id)}/spectator`
            : `/v1/rooms/${encodeURIComponent(id)}/view`,
          { signal: abort.signal },
        );
        if (active && !socketSnapshot) {
          const normalized = decode(data);
          finished = terminalState(normalized.status, normalized.phase).ended;
          setView(normalized);
          setError("");
        }
      } catch (e) {
        if (
          active &&
          !socketSnapshot &&
          !(e instanceof DOMException && e.name === "AbortError")
        ) {
          if (e instanceof IncompatibleViewError) {
            rejectIncompatible(e);
            return;
          }
          if (e instanceof APIError && e.status === 410) {
            gone = true;
            finished = true;
            token.current = "";
            setArchived(true);
            setView(undefined);
            setError("");
            setControlStatus("readonly");
            if (retry) clearTimeout(retry);
            socket?.close();
            return;
          }
          setError(errorMessage(e));
          if (e instanceof APIError && (e.status === 401 || e.status === 403)) {
            token.current = "";
            setControlStatus("readonly");
            setView(undefined);
          }
        }
      } finally {
        loading = false;
      }
    };
    const connect = async () => {
      if (gone) return;
      try {
        const ticket = spectator
          ? await post<{ ticket: string }>(
              `/v1/public/rooms/${encodeURIComponent(id)}/spectator-ticket`,
            )
          : undefined;
        if (!active || gone) return;
        socket = new WebSocket(
          `${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}/v1/ws/${spectator ? "spectators" : "players"}?room_id=${encodeURIComponent(id)}`,
        );
        socketRef.current = socket;
        resumePending.current = false;
        socket.onopen = () => {
          if (ticket)
            socket?.send(
              JSON.stringify({ type: "authenticate", ticket: ticket.ticket }),
            );
          if (active) {
            setConnected(true);
            retries = 0;
            void load();
          }
        };
        socket.onmessage = (event) => {
          if (!active || gone) return;
          let message: WireFrame;
          try {
            message = JSON.parse(String(event.data));
          } catch {
            return;
          }
          switch (message.type) {
            case "control_granted":
              if (typeof message.control_token === "string") {
                token.current = message.control_token;
                resumePending.current = false;
                setControlStatus("active");
              }
              break;
            case "control_readonly":
              if (token.current && !resumePending.current) {
                resumePending.current = true;
                socket?.send(
                  JSON.stringify({
                    type: "resume_control",
                    control_token: token.current,
                  }),
                );
              } else {
                token.current = "";
                resumePending.current = false;
                setControlStatus("readonly");
              }
              break;
            case "control_changed":
              token.current = "";
              setControlStatus("readonly");
              privateSnapshot = undefined;
              break;
            case "heartbeat":
              socket?.send(JSON.stringify({ type: "heartbeat" }));
              break;
            case "snapshot":
            case "spectator_snapshot":
            case "room_snapshot": {
              const stream =
                typeof message.stream_id === "string" ? message.stream_id : "";
              const seq =
                typeof message.view_seq === "number" ? message.view_seq : 0;
              if (stream && stream === lastStream && seq <= lastSeq) break;
              let normalized: SpectatorView | ParticipantView;
              try {
                normalized = decode(message);
              } catch (error) {
                if (error instanceof IncompatibleViewError)
                  rejectIncompatible(error);
                return;
              }
              lastStream = stream;
              lastSeq = seq;
              socketSnapshot = true;
              finished = terminalState(
                typeof message.status === "string" ? message.status : undefined,
                undefined,
              ).ended;
              setError("");
              // Public messages are projected immediately; no raw spectator payload enters React state or a replay buffer.
              if (!spectator) privateSnapshot = message;
              setView(normalized);
              break;
            }
            case "decision_request":
              if (!spectator) {
                const next = attachDecision(privateSnapshot, message);
                if (next) {
                  lastSeq = Number(message.view_seq);
                  setView(normalizeParticipant(next));
                } else {
                  privateSnapshot = undefined;
                  setView((v) =>
                    v && "hand" in v ? { ...v, decision: undefined } : v,
                  );
                  setError("决策观测已失效，正在重新同步。");
                  socket?.send(JSON.stringify({ type: "resume" }));
                }
              }
              break;
            case "seat_assigned":
              if (!spectator && privateSnapshot?.hand_id !== message.hand_id) {
                privateSnapshot = undefined;
                setView((v) =>
                  v && "hand" in v ? { ...v, decision: undefined } : v,
                );
              }
              break;
          }
        };
        socket.onerror = () => socket?.close();
        socket.onclose = () => {
          if (active) {
            socketSnapshot = false;
            privateSnapshot = undefined;
            lastStream = "";
            lastSeq = 0;
            setConnected(false);
            setControlStatus((c) => (c === "readonly" ? c : "connecting"));
            if (!finished)
              retry = setTimeout(
                () => void connect(),
                Math.min(1000 * 2 ** retries++, 15000),
              );
          }
        };
      } catch {
        if (active)
          if (!finished)
            retry = setTimeout(
              () => void connect(),
              Math.min(1000 * 2 ** retries++, 15000),
            );
      }
    };
    void load();
    void connect();
    const interval = setInterval(() => {
      if (document.visibilityState === "visible") void load();
    }, 2000);
    const onFocus = () => void load();
    window.addEventListener("focus", onFocus);
    return () => {
      active = false;
      abort?.abort();
      socket?.close();
      if (retry) clearTimeout(retry);
      clearInterval(interval);
      window.removeEventListener("focus", onFocus);
    };
  }, [id, spectator, nonce]);
  return {
    view,
    error,
    connected,
    archived,
    incompatible,
    refresh,
    controlStatus,
    takeControl,
    actionHeaders,
  };
}
