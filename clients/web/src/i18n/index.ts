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
  // --- game UI (step 5) ---
  "board.aria": "Chess board",
  "board.square.aria": "square",
  "board.empty": "empty",
  "piece.white": "white",
  "piece.black": "black",
  "piece.king": "king",
  "piece.queen": "queen",
  "piece.rook": "rook",
  "piece.bishop": "bishop",
  "piece.knight": "knight",
  "piece.pawn": "pawn",
  "promotion.aria": "Choose promotion piece",
  "announce.your_move": "Your move.",
  "announce.opponent_moved": "Opponent played",
  "announce.you_moved": "You played",
  "announce.draw_offered": "Your opponent offers a draw.",
  "announce.takeback_requested": "Your opponent asks to take back a move.",
  "announce.game_over": "Game over.",
  "game.turn.yours": "Your move",
  "game.turn.theirs": "Opponent to move",
  "game.resign": "Resign",
  "game.offerDraw": "Offer draw",
  "game.draw.offered": "Opponent offers a draw.",
  "game.draw.accept": "Accept draw",
  "game.draw.decline": "Decline draw",
  "game.draw.sent": "Draw offered. Waiting for your opponent.",
  "game.takeback": "Takeback",
  "game.takeback.requested": "Opponent asks to take back a move.",
  "game.takeback.accept": "Accept takeback",
  "game.takeback.decline": "Decline takeback",
  "game.takeback.sent": "Takeback requested. Waiting for your opponent.",
  "game.clock.aria": "Remaining time",
  "game.opponent.label": "Opponent",
  "game.resync": "Reconnecting game state…",
  "game.reject.illegal": "Illegal move.",
  "game.reject.stale": "Move out of sync — try again.",
  "game.reject.gameover": "The game is over.",
  "game.reject.notyourturn": "Not your turn.",
  "game.reject.notyourseat": "Not your seat.",
  "game.reject.unknown": "Move rejected.",
  "game.result.win": "You won.",
  "game.result.loss": "You lost.",
  "game.result.draw": "Draw.",
  "game.result.over": "Game over.",
  "error.unsupported_version": "This client is out of date — please refresh the page.",
  "error.generic": "A connection error occurred.",
} as const;

export type MessageId = keyof typeof en;

// The active catalog. Deferred ADR-7 locale selection replaces this binding.
const catalog: Record<MessageId, string> = en;

/** Resolve a user-facing string by id. The ONLY way a component gets display text. */
export function t(id: MessageId): string {
  return catalog[id];
}
