import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import fixture from "../../api/fixtures/decision.json";
import { useTable } from "./useTable";
import { clearAuthState } from "./api";
class Socket {
  static OPEN = 1;
  static instances: Socket[] = [];
  readyState = 1;
  sent: string[] = [];
  onopen?: () => void;
  onclose?: () => void;
  onerror?: () => void;
  onmessage?: (event: { data: string }) => void;
  constructor(public url: string) {
    Socket.instances.push(this);
  }
  send(data: string) {
    this.sent.push(data);
  }
  close() {
    this.readyState = 3;
  }
  frame(value: unknown) {
    this.onmessage?.({ data: JSON.stringify(value) });
  }
}
beforeEach(() => {
  Socket.instances = [];
  clearAuthState();
  vi.stubGlobal("WebSocket", Socket);
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async (path: string) =>
        new Response(
          JSON.stringify(
            path.endsWith("take-control")
              ? { control_token: "explicit-tab-token", control_epoch: 8 }
              : { ...fixture.snapshot, decision: fixture.decision },
          ),
          { status: 200 },
        ),
    ),
  );
});
afterEach(() => {
  vi.unstubAllGlobals();
  clearAuthState();
});
describe("tab-scoped websocket control and observation handshake", () => {
  it("takes over explicitly, resumes on the existing connection and loses all write authority when replaced", async () => {
    const { result, unmount } = renderHook(() => useTable("room_test", false));
    await waitFor(() => expect(Socket.instances).toHaveLength(1));
    const socket = Socket.instances[0];
    act(() => {
      socket.onopen?.();
      socket.frame({ type: "control_readonly", control_epoch: 7 });
    });
    expect(result.current.controlStatus).toBe("readonly");
    expect(result.current.actionHeaders()["X-Control-Token"]).toBe("");
    await act(async () => {
      await result.current.takeControl();
    });
    expect(socket.sent.map((v) => JSON.parse(v))).toContainEqual({
      type: "resume_control",
      control_token: "explicit-tab-token",
    });
    expect(socket.url).not.toContain("explicit-tab-token");
    expect(result.current.controlStatus).toBe("connecting");
    act(() =>
      socket.frame({
        type: "control_granted",
        control_token: "explicit-tab-token",
        control_epoch: 8,
      }),
    );
    expect(result.current.controlStatus).toBe("active");
    expect(result.current.actionHeaders()["X-Control-Token"]).toBe(
      "explicit-tab-token",
    );
    act(() => socket.frame({ type: "control_changed", control_epoch: 9 }));
    expect(result.current.controlStatus).toBe("readonly");
    expect(result.current.actionHeaders()["X-Control-Token"]).toBe("");
    unmount();
  });
  it("maps matching private snapshot and decision frames and requests recovery for stale references", async () => {
    const { result, unmount } = renderHook(() => useTable("room_test", false));
    await waitFor(() => expect(Socket.instances).toHaveLength(1));
    const socket = Socket.instances[0];
    act(() => {
      socket.frame(fixture.snapshot);
      socket.frame(fixture.decision);
    });
    expect(
      result.current.view && "decision" in result.current.view
        ? result.current.view.decision?.decision_id
        : undefined,
    ).toBe("decision_1");
    act(() => socket.frame({ ...fixture.decision, hand_id: "old_hand" }));
    expect(
      result.current.view && "decision" in result.current.view
        ? result.current.view.decision
        : undefined,
    ).toBeUndefined();
    expect(socket.sent.map((v) => JSON.parse(v))).toContainEqual({
      type: "resume",
    });
    unmount();
  });
});

describe("expired public records", () => {
  it("stops polling and reconnecting when the host returns MATCH_ARCHIVED", async () => {
    vi.useFakeTimers();
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(JSON.stringify({ error: { code: "MATCH_ARCHIVED" } }), {
            status: 410,
          }),
      ),
    );
    const { result, unmount } = renderHook(() =>
      useTable("archived_room", false),
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(50);
    });
    expect(result.current.archived).toBe(true);
    const calls = vi.mocked(fetch).mock.calls.length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10000);
    });
    expect(vi.mocked(fetch).mock.calls.length).toBe(calls);
    expect(Socket.instances.every((s) => s.readyState === 3)).toBe(true);
    unmount();
    vi.useRealTimers();
  });
});

it("clears the private view and authority when a new snapshot needs an unknown renderer version", async () => {
  const { result, unmount } = renderHook(() => useTable("room_test", false));
  await waitFor(() => expect(Socket.instances).toHaveLength(1));
  const socket = Socket.instances[0];
  act(() => {
    socket.frame({
      type: "control_granted",
      control_token: "tab-token",
      control_epoch: 7,
    });
    socket.frame(fixture.snapshot);
    socket.frame(fixture.decision);
  });
  expect(result.current.view).toBeDefined();
  act(() =>
    socket.frame({
      ...fixture.snapshot,
      view_seq: 3,
      view: {
        ...fixture.snapshot.view,
        ruleset: { id: "openmajiang.mcr", version: "2.0.0" },
      },
    }),
  );
  expect(result.current.incompatible).toBe(true);
  expect(result.current.view).toBeUndefined();
  expect(result.current.actionHeaders()["X-Control-Token"]).toBe("");
  expect(result.current.controlStatus).toBe("readonly");
  expect(socket.readyState).toBe(3);
  unmount();
});
