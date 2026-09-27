import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { Bots } from "./Bots";
const mock = vi.hoisted(() => ({
  api: vi.fn(),
  bot: {
    id: "b1",
    name: "Runner Bot",
    online: true,
    enabled: true,
    suspended: false,
    current_version: "version_1",
  },
}));
vi.mock("./hooks", () => ({
  useResource: (path: string) => ({
    data:
      path === "/v1/bots"
        ? { bots: [mock.bot] }
        : path?.endsWith("/status")
          ? {
              online: mock.bot.online,
              connected: false,
              queued: false,
              recent_errors: [],
            }
          : path?.endsWith("/versions")
            ? { versions: [{ id: "version_1", label: "v1" }] }
            : { matches: [], credentials: [] },
    loading: false,
    error: "",
    reload: vi.fn(),
  }),
}));
vi.mock("./api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./api")>()),
  api: mock.api,
}));
const user = {
  id: "u1",
  display_name: "本人",
  email: "me@example.test",
  email_verified: true,
};
beforeEach(() => {
  mock.api.mockReset().mockResolvedValue({});
  Object.assign(mock.bot, { online: true, enabled: true, suspended: false });
});
it("allows a live polling runner from the actual online DTO to enter the queue without requiring an existing table WebSocket", async () => {
  render(<Bots user={user} />);
  await userEvent.click(screen.getByRole("button", { name: /Runner Bot/ }));
  const join = screen.getByRole("button", { name: "加入公共队列" });
  expect((join as HTMLButtonElement).disabled).toBe(false);
  await userEvent.click(join);
  expect(mock.api).toHaveBeenCalledWith(
    "/v1/bots/b1/queue",
    expect.objectContaining({ method: "POST" }),
  );
  expect(JSON.parse(mock.api.mock.calls[0][1].body)).toMatchObject({
    ruleset_id: "openmajiang.mcr",
    match_format: "standard_16",
    continuous: true,
  });
});
it.each(["offline", "disabled", "suspended"])(
  "disables queue admission while the Bot is %s",
  async (state) => {
    if (state === "offline") mock.bot.online = false;
    if (state === "disabled") mock.bot.enabled = false;
    if (state === "suspended") mock.bot.suspended = true;
    render(<Bots user={user} />);
    await userEvent.click(screen.getByRole("button", { name: /Runner Bot/ }));
    expect(
      (
        screen.getByRole("button", {
          name: "加入公共队列",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    expect(mock.api).not.toHaveBeenCalled();
  },
);
