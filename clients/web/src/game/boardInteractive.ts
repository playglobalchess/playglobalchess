// The board accepts input only on our turn, with no move pending, and while the
// position is in sync (SPEC-S4-WEB-F01 §3.4.1: read-only while SYNCING,
// RECONNECTING or FAILED). Pure so the gate is unit-testable without the App.

import type { SyncPhase } from "./reducer";

export function boardInteractive(
  state: { pendingMove: unknown; sync: { phase: SyncPhase } },
  myTurn: boolean,
): boolean {
  return myTurn && state.pendingMove == null && state.sync.phase === "IN_SYNC";
}
