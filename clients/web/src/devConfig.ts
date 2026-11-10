// Shared dev matchmaking constants for first-playable (F-01). Both the App UI
// AND the real-server e2e import these, so the e2e exercises the EXACT values a
// human sends — a malformed time control (the "300+0" bug that the server
// rejected with "could not start game") then fails the e2e in CI rather than
// only surfacing in a manual browser run.
//
// The pool MUST be one the server knows, and the time control MUST be that
// pool's (the server refuses anything else, #93). In the interim catalogue
// (transport.DefaultPools) each pool id IS its time-control id: "blitz-5+0" =
// 5 minutes, no increment. A pool/time-control picker UI is a later slice.
export const DEV_POOL = "blitz-5+0";
export const DEV_TIME_CONTROL = "blitz-5+0";
