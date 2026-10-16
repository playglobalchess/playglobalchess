// Shared dev matchmaking constants for first-playable (F-01). Both the App UI
// AND the real-server e2e import these, so the e2e exercises the EXACT values a
// human sends — a malformed time control (the "300+0" bug that the server
// rejected with "could not start game") then fails the e2e in CI rather than
// only surfacing in a manual browser run.
//
// The time control MUST match the server resolver's format
// "<name>-<base_min>+<inc_sec>" (ConventionResolver): "blitz-5+0" = 5 minutes,
// no increment. A pool/time-control picker UI is a later slice.
export const DEV_POOL = "blitz";
export const DEV_TIME_CONTROL = "blitz-5+0";
