import { describe, expect, it } from "vitest";
import fixture from "../../api/fixtures/decision.json";
import publicFixture from "../../api/fixtures/spectator.json";
import {
  assertViewCompatibility,
  IncompatibleViewError,
} from "./compatibility";
import { normalizeSpectator } from "./spectator";
import { normalizeParticipant } from "./table-state";
import { attachDecision } from "./stream";
describe("explicit view and rule compatibility", () => {
  it("accepts the shared Go public and private projection contracts", () => {
    expect(() =>
      assertViewCompatibility(fixture.snapshot, false),
    ).not.toThrow();
    expect(normalizeSpectator(publicFixture).discards[0].tile).toBe("1m");
  });
  it("rejects private policy on a public route, unknown rule versions and absent metadata", () => {
    expect(() => normalizeSpectator(fixture.snapshot)).toThrow(
      IncompatibleViewError,
    );
    expect(() =>
      normalizeParticipant({
        ...fixture.snapshot,
        view: {
          ...fixture.snapshot.view,
          ruleset: { id: "another.rule", version: "1.0.0" },
        },
      }),
    ).toThrow(IncompatibleViewError);
    expect(() =>
      normalizeParticipant({ view: { hand: [], discards: [], seats: [] } }),
    ).toThrow(IncompatibleViewError);
  });
  it("rejects decisions requiring a different protocol or rules renderer", () => {
    expect(
      attachDecision(fixture.snapshot, {
        ...fixture.decision,
        protocol_version: "2.0",
      }),
    ).toBeUndefined();
    expect(
      attachDecision(fixture.snapshot, {
        ...fixture.decision,
        ruleset: { id: "openmajiang.mcr", version: "2.0.0" },
      }),
    ).toBeUndefined();
  });
  it("accepts only the explicit waiting room envelope when no hand exists", () => {
    expect(() =>
      assertViewCompatibility(
        { type: "room_snapshot", view: null, room: fixture.snapshot.room },
        true,
      ),
    ).not.toThrow();
    expect(() => assertViewCompatibility({ view: null }, true)).toThrow(
      IncompatibleViewError,
    );
  });
});
