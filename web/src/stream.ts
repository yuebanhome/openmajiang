import { assertViewCompatibility, supportedRuleset } from "./compatibility";
export type WireFrame = Record<string, unknown>;
const fields = [
  "match_id",
  "hand_id",
  "participant_id",
  "seat_id",
  "seat_assignment_version",
  "control_epoch",
] as const;
// Decision frames may only attach to the exact private observation identified by the host.
export function attachDecision(
  snapshot: WireFrame | undefined,
  decision: WireFrame,
): WireFrame | undefined {
  if (
    !snapshot ||
    decision.type !== "decision_request" ||
    !decision.observation_ref ||
    typeof decision.observation_ref !== "object"
  )
    return;
  try {
    assertViewCompatibility(snapshot, false);
  } catch {
    return;
  }
  if (
    decision.protocol_version !== "1.0" ||
    !supportedRuleset(decision.ruleset)
  )
    return;
  const ref = decision.observation_ref as WireFrame;
  if (
    ref.stream_id !== snapshot.stream_id ||
    ref.view_seq !== snapshot.view_seq ||
    decision.stream_id !== snapshot.stream_id
  )
    return;
  if (
    typeof decision.view_seq !== "number" ||
    typeof snapshot.view_seq !== "number" ||
    decision.view_seq <= snapshot.view_seq
  )
    return;
  if (fields.some((field) => decision[field] !== snapshot[field])) return;
  return { ...snapshot, decision };
}
