import type { SpectatorView, Discard } from './types';
const tilePattern = /^(?:[1-9][mps]|[1-7]z|[1-8]h|h[1-8])$/;
const asObject = (v: unknown): Record<string, unknown> => v !== null && typeof v === 'object' && !Array.isArray(v) ? v as Record<string, unknown> : {};
const string = (v: unknown) => typeof v === 'string' ? v : '';
const number = (v: unknown) => typeof v === 'number' && Number.isFinite(v) ? v : 0;
// A new allowlisted object. No spreads, concealed fields, physical tile IDs, melds or results.
export function normalizeSpectator(input: unknown): SpectatorView {
  const root = asObject(input);
  const view = asObject(root.view ?? input);
  const discards: Discard[] = [];
  const items = Array.isArray(view.discards) ? view.discards : [];
  for (const [index,item] of items.entries()) {
    const d = asObject(item);
    const tile = string(d.kind);
    if (!tilePattern.test(tile)) continue;
    discards.push({ tile, seat_id: number(d.from_seat), sequence: index + 1, claimed: d.claimed === true, hand_index: number(view.hand_index) });
  }
  const scores = (Array.isArray(view.scores) ? view.scores : []).map(asObject);
  const players = (Array.isArray(view.seats) ? view.seats : []).map(raw => {
    const p = asObject(raw);
    const score = scores.find(s => s.participant_id === p.participant_id);
    return { seat_id: number(p.seat_id), name: string(p.name), score: number(score?.total) };
  });
  return { match_id: string(root.match_id), phase: string(view.phase), hand_index: number(view.hand_index), discards, players, status: string(root.status) };
}
