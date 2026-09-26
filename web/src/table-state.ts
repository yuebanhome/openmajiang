import type { ParticipantView, Tile, Discard, ActionOption } from './types';
const object = (v: unknown): Record<string, unknown> => v && typeof v === 'object' && !Array.isArray(v) ? v as Record<string, unknown> : {};
const array = (v: unknown): unknown[] => Array.isArray(v) ? v : [];
const text = (v: unknown) => typeof v === 'string' ? v : '';
const num = (v: unknown) => typeof v === 'number' ? v : 0;
const tile = (raw:unknown):Tile => { const t=object(raw); return {id:text(t.tile_id),kind:text(t.kind)}; };
export function normalizeParticipant(input: unknown): ParticipantView {
 const root=object(input);const v=object(root.view ?? input); const hand=array(v.hand).map(tile);
 const discards=array(v.discards).map((raw,index)=>{const d=object(raw);return{tile:text(object(d.tile).kind),seat_id:num(d.from_seat),sequence:index+1,claimed:d.claimed===true} satisfies Discard;});
 const scores=array(v.scores).map(object);
 const players=array(v.seats).map(raw=>{const p=object(raw);const score=scores.find(s=>s.participant_id===p.participant_id);return{seat_id:num(p.seat_id),seat_wind:num(p.seat_wind),name:text(p.name),score:num(score?.total),hand_count:num(p.hand_count),melds:array(p.melds).map(rawM=>{const m=object(rawM);return{type:text(m.type),tiles:array(m.tiles).map(tile)}}),flowers:array(p.flowers).map(f=>text(object(f).kind))};});
 const d=object(root.decision); const actions=array(d.legal_actions).map(raw=>{const a=object(raw);return{option_id:text(a.option_id),type:text(a.type),tile_id:text(a.tile_id),kind:text(a.kind),consume_tile_ids:array(a.consume_tile_ids).map(text)} satisfies ActionOption;});
 const decisionID=text(d.decision_id); const seatID=num(root.seat_id ?? v.seat_id); const result=object(v.result);
 return {match_id:text(root.match_id ?? d.match_id),phase:text(v.phase),hand_index:num(v.hand_index),hand_id:text(root.hand_id ?? d.hand_id),window_id:text(d.window_id),control_epoch:num(root.control_epoch ?? d.control_epoch),participant_id:text(root.participant_id ?? v.participant_id),seat_id:seatID,seat_assignment_version:num(root.seat_assignment_version ?? d.seat_assignment_version),hand,discards,players,decision:decisionID?{decision_id:decisionID,seat_id:num(d.seat_id),legal_actions:actions,deadline_at:text(d.deadline_at)}:undefined,deadline_at:text(root.deadline_at),status:text(root.status),outcome:v.result?{reason:text(result.method),fan:num(result.total_fan),nonflower:num(result.non_flower_fan),flowers:num(result.flower_points),winning_form:text(result.winning_form),decomposition:array(result.decomposition).map(raw=>{const g=object(raw);return{kind:text(g.kind),tiles:array(g.tiles).map(text)}}),explanations:array(result.explanations).map(raw=>text(object(raw).reason)),breakdown:array(result.fan_items).map(raw=>{const f=object(raw);return{name:text(f.name),points:num(f.points),count:num(f.count)}})}:undefined};
}
