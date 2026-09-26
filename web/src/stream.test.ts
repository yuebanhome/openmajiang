import { describe, expect, it } from "vitest";
import fixture from "../../api/fixtures/decision.json";
import { attachDecision } from "./stream";
import { normalizeParticipant } from "./table-state";
describe("shared Go / TypeScript / Python decision fixture", () => {
  it("attaches only the exact referenced snapshot, preserving all submission authority", () => {
    const combined = attachDecision(fixture.snapshot, fixture.decision);
    expect(combined).toBeDefined();
    const view = normalizeParticipant(combined);
    expect(view.hand).toHaveLength(14);
    expect(view.hand[0].id).toBe("tile_1");
    expect(view.decision?.legal_actions[0]).toMatchObject({
      option_id: "discard_1",
      tile_id: "tile_1",
    });
    expect(view.match_id).toBe(fixture.decision.match_id);
    expect(view.hand_id).toBe(fixture.decision.hand_id);
    expect(view.window_id).toBe(fixture.decision.window_id);
    expect(view.control_epoch).toBe(fixture.decision.control_epoch);
    expect(view.seat_assignment_version).toBe(
      fixture.decision.seat_assignment_version,
    );
  });
  it.each([
    "match_id",
    "hand_id",
    "participant_id",
    "seat_id",
    "seat_assignment_version",
    "control_epoch",
  ])("rejects a decision with a different %s", (field) => {
    expect(
      attachDecision(fixture.snapshot, {
        ...fixture.decision,
        [field]: "stale",
      }),
    ).toBeUndefined();
  });
  it("does not attach a decision to an earlier hand or an unobserved stream position", () => {
    expect(attachDecision(undefined, fixture.decision)).toBeUndefined();
    expect(
      attachDecision(fixture.snapshot, {
        ...fixture.decision,
        observation_ref: { ...fixture.decision.observation_ref, view_seq: 99 },
      }),
    ).toBeUndefined();
    expect(
      attachDecision(fixture.snapshot, {
        ...fixture.decision,
        stream_id: "other_stream",
      }),
    ).toBeUndefined();
  });
});
