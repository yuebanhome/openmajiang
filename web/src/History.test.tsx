import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { History, Replay } from "./History";
const user = {
  id: "u1",
  display_name: "本人",
  email: "player@example.test",
  email_verified: true,
};
const json = (data: unknown, status = 200) =>
  new Response(JSON.stringify(data), { status });
afterEach(() => vi.unstubAllGlobals());
describe("history boundaries and archival", () => {
  it("shows retained settlement metadata for an archived replay and never creates tile or replay controls", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (path: string) =>
        path.includes("/replay")
          ? json({ error: { code: "MATCH_ARCHIVED" } }, 410)
          : json({
              archived: true,
              match: { id: "m1", match_format: "standard_16" },
              summary: {
                version: "settled-scores@1",
                completed_hands: 16,
                scores: [
                  {
                    participant_id: "p1",
                    kind: "human",
                    total: 34,
                    rank: 1,
                    standard_points: { numerator: 7, denominator: 2 },
                  },
                ],
              },
            }),
      ),
    );
    const { container } = render(<Replay id="m1" privateView={false} />);
    await screen.findByRole("heading", { name: "详细牌谱已归档" });
    expect(screen.getByText("34", { exact: true })).toBeTruthy();
    expect(screen.getByText("3.5", { exact: true })).toBeTruthy();
    expect(container.querySelectorAll(".tile,.replay-controls")).toHaveLength(
      0,
    );
  });
  it("selects only owned Bot identities and ignores a delayed response from the previous Bot view", async () => {
    let resolveOld: (value: Response) => void = () => {};
    const old = new Promise<Response>((resolve) => {
      resolveOld = resolve;
    });
    const frame = (kind: string) => ({
      frames: [
        {
          seq: 1,
          view: {
            ruleset: { id: "openmajiang.mcr", version: "1.0.0" },
            view_policy: "participant_private@1",
            phase: "self",
            hand_index: 1,
            hand: [{ tile_id: "private-tile", kind }],
            seats: [],
            discards: [],
          },
        },
      ],
      next_after: 1,
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (path: string) => {
        if (path === "/v1/bots")
          return json({
            bots: [
              { id: "b1", name: "第一个 Bot" },
              { id: "b2", name: "第二个 Bot" },
            ],
          });
        if (path.includes("/replay"))
          return new URL(path, "http://localhost").searchParams.get(
            "bot_id",
          ) === "b1"
            ? old
            : json(frame("2m"));
        return json({ match: { id: "m1", match_format: "practice_1" } });
      }),
    );
    const { container } = render(
      <Replay id="m1" privateView user={user} initialBotID="b1" />,
    );
    await screen.findByRole("option", { name: "第二个 Bot（我的 Bot）" });
    await userEvent.selectOptions(screen.getByLabelText("复盘身份"), "b2");
    await waitFor(() =>
      expect(
        container
          .querySelector(".hand-tiles .tile")
          ?.getAttribute("aria-label"),
      ).toBe("2萬"),
    );
    await act(async () => resolveOld(json(frame("9m"))));
    expect(
      container.querySelector(".hand-tiles .tile")?.getAttribute("aria-label"),
    ).toBe("2萬");
  });
  it("preserves an opaque pagination cursor and links owned Bot records to their explicitly authorized replay identity", async () => {
    const fetch = vi.fn(async (path: string) => {
      if (path === "/v1/bots")
        return json({ bots: [{ id: "b1", name: "自己的 Bot" }] });
      const u = new URL(path, "http://localhost");
      return json({
        matches: [
          {
            id: u.searchParams.has("before") ? "m2" : "m1",
            mode: "bot_only",
            match_format: "practice_1",
            status: "completed",
            ruleset_version: "1.0.0",
          },
        ],
        next_before: u.searchParams.has("before") ? "" : "opaque+/=",
      });
    });
    vi.stubGlobal("fetch", fetch);
    render(<History user={user} />);
    await screen.findByRole("option", { name: "自己的 Bot" });
    await userEvent.selectOptions(
      screen.getByLabelText("选择自己的 Bot 对局"),
      "b1",
    );
    await waitFor(() =>
      expect(
        document.querySelector(".history-card")?.getAttribute("href"),
      ).toBe("/replays/m1?bot_id=b1"),
    );
    await userEvent.click(
      screen.getByRole("button", { name: "加载更早的对局" }),
    );
    await waitFor(() =>
      expect(document.querySelectorAll(".history-card")).toHaveLength(2),
    );
    const url = fetch.mock.calls
      .map(([path]) => new URL(path, "http://localhost"))
      .find((u) => u.searchParams.has("before"));
    expect(url?.searchParams.get("before")).toBe("opaque+/=");
    expect(url?.searchParams.get("bot_id")).toBe("b1");
  });
});
