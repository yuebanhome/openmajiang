import { describe, expect, it } from "vitest";
import { terminalState } from "./presentation";
describe("platform terminal status takes priority over a stopped engine phase", () => {
  it.each([
    "aborted_by_server",
    "missing_ruleset",
    "invalid_state",
    "ended_early",
  ])("explains %s even while the last engine phase is self", (status) => {
    expect(terminalState(status, "self")).toMatchObject({
      ended: true,
      interrupted: true,
    });
    expect(terminalState(status, "self").reason).not.toBe("");
  });
  it("distinguishes complete games from intermission", () => {
    expect(terminalState("completed", "ended")).toMatchObject({
      ended: true,
      interrupted: false,
    });
    expect(terminalState("active", "intermission").ended).toBe(false);
    expect(terminalState("cancelled", undefined)).toMatchObject({ended:true,interrupted:false,reason:'房间已关闭'});
  });
});
