type ObjectValue = Record<string, unknown>;
const object = (v: unknown): ObjectValue =>
  v && typeof v === "object" && !Array.isArray(v) ? (v as ObjectValue) : {};
export class IncompatibleViewError extends Error {
  constructor() {
    super(
      "当前页面不支持这份规则版本或牌面协议。请刷新页面；若仍有提示，请等待平台完成更新。",
    );
  }
}
export function supportedRuleset(value: unknown): boolean {
  const rule = object(value);
  return rule.id === "openmajiang.mcr" && rule.version === "1.0.0";
}
// Only metadata is inspected here; spectator projection never touches concealed fields.
export function assertViewCompatibility(
  input: unknown,
  spectator: boolean,
): void {
  const root = object(input);
  if (root.type === "room_snapshot" && root.view === null) {
    const room = object(root.room);
    if (
      room.ruleset_id === "openmajiang.mcr" &&
      room.ruleset_version === "1.0.0"
    )
      return;
    throw new IncompatibleViewError();
  }
  const view = object(root.view ?? input);
  const policy = spectator
    ? "spectator_discard_only@1"
    : "participant_private@1";
  if (
    !supportedRuleset(view.ruleset) ||
    view.view_policy !== policy ||
    (root.ruleset !== undefined && !supportedRuleset(root.ruleset)) ||
    (root.view_policy !== undefined && root.view_policy !== policy)
  )
    throw new IncompatibleViewError();
}
