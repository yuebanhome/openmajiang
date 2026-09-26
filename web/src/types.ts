export type User = {
  id: string;
  email: string;
  display_name: string;
  email_verified: boolean;
  role?: string;
};
export type RuleManifest = {
  id: string;
  version: string;
  name: string;
  renderer_id?: string;
  seat_counts: number[];
  formats: string[];
  capabilities?: string[];
};
export type Seat = {
  participant_id?: string;
  user_id?: string;
  bot_id?: string;
  seat_id?: number;
  display_name?: string;
  name?: string;
  kind?: string;
  ready?: boolean;
  score?: number;
  connected?: boolean;
};
export type Room = {
  id: string;
  name: string;
  mode: string;
  status: string;
  access?: string;
  ruleset_id: string;
  ruleset_version: string;
  match_format: string;
  seats: Seat[];
  owner_id?: string;
  match_id?: string;
  invite_code?: string;
  hand_number?: number;
  capacity?: number;
};
export type Discard = {
  tile: string;
  kind?: string;
  seat_id: number;
  sequence: number;
  claimed?: boolean;
  hand_index?: number;
};
// This type is deliberately not related to ParticipantView. Only normalizeSpectator may populate it.
export type SpectatorView = {
  match_id: string;
  phase: string;
  hand_index: number;
  discards: Discard[];
  players: {
    seat_id: number;
    seat_wind?: number;
    name: string;
    score: number;
  }[];
  status: string;
};
export type Tile = { id: string; kind: string };
export type ActionOption = {
  option_id: string;
  type: string;
  tile_id?: string;
  kind?: string;
  consume_tile_ids?: string[];
  details?: unknown;
};
export type Decision = {
  decision_id: string;
  participant_id?: string;
  seat_id: number;
  legal_actions: ActionOption[];
  deadline_at?: string;
};
export type ParticipantView = {
  match_id: string;
  phase: string;
  hand_index: number;
  hand_id?: string;
  window_id?: string;
  control_epoch?: number;
  participant_id?: string;
  seat_id: number;
  seat_assignment_version: number;
  hand: Tile[];
  discards: Discard[];
  players: {
    seat_id: number;
    seat_wind?: number;
    name: string;
    score: number;
    hand_count?: number;
    melds?: { type: string; tiles: Tile[] }[];
    flowers?: string[];
  }[];
  decision?: Decision;
  legal_actions?: ActionOption[];
  decision_id?: string;
  deadline_at?: string;
  scores?: unknown;
  status: string;
  outcome?: {
    winner?: string;
    reason?: string;
    fan?: number;
    nonflower?: number;
    flowers?: number;
    winning_form?: string;
    breakdown?: { name: string; points: number; count: number }[];
    decomposition?: { kind: string; tiles: string[] }[];
    explanations?: string[];
  };
};
export type Bot = {
  online?: boolean;
  suspended?: boolean;
  enabled?: boolean;
  current_version?: string;
  id: string;
  name: string;
  description?: string;
  connected?: boolean;
  status?: string;
  versions?: {
    id: string;
    name?: string;
    version?: string;
    created_at?: string;
  }[];
  credentials?: {
    id: string;
    prefix?: string;
    created_at?: string;
    revoked_at?: string;
  }[];
  matches?: number;
};
export type MatchRecord = {
  id: string;
  room_id?: string;
  room_name?: string;
  status: string;
  mode: string;
  match_format: string;
  ruleset_id: string;
  ruleset_version: string;
  hand_index?: number;
  created_at?: string;
  ended_at?: string;
  players?: { name: string; score: number; rank?: number }[];
};
