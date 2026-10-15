// Minimal i18n layer (NFR-11): all user-facing strings live in a typed catalog
// keyed by a message id; components call t(id) — never an inline literal. The
// launch locale list is deferred (ADR-7); this slice ships English only, but the
// INDIRECTION is in place from the first commit so there are no hard-coded
// strings to retrofit. A later slice swaps the single catalog for a locale
// loader; the call sites do not change.

export const en = {
  "app.title": "PlayGlobalChess",
  "app.tagline": "First-playable (F-01) — Standard chess vs the live server.",
  "connect.token.label": "Dev token",
  "connect.token.placeholder": "paste a dev token (temporary until accounts land)",
  "connect.action": "Connect",
  "seek.action": "Find a game",
  "status.disconnected": "Disconnected",
  "status.connecting": "Connecting…",
  "status.connected": "Connected",
  "status.seeking": "Finding an opponent…",
  "status.in_game": "Game in progress",
  "status.game_over": "Game over",
  "codec.version.label": "Shared §4A codec, protocol v",
  "codec.version.aria": "Shared protocol codec version",
} as const;

export type MessageId = keyof typeof en;

// The active catalog. Deferred ADR-7 locale selection replaces this binding.
const catalog: Record<MessageId, string> = en;

/** Resolve a user-facing string by id. The ONLY way a component gets display text. */
export function t(id: MessageId): string {
  return catalog[id];
}
