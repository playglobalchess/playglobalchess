// The TS binding to the ONE shared §4A codec (pgc-protocol-wasm, compiled from
// the pure Rust codec — NFR-5/NFR-10). This module is the ONLY place the web
// client touches the wasm seam; the rest of the app speaks typed messages.
//
// Step 1 exposes just the version probe to prove the wasm import chain builds
// and runs. Step 2 adds the typed encode(ClientMsg)/decode(ServerMsg) wrappers
// over the seam's encodeClient/decodeServer (keeping §4A bytes Rust-produced).

import { protocol_version } from "pgc-protocol-wasm";

/** The §4A protocol major version the shared codec speaks (§4A.1). */
export function codecProtocolVersion(): number {
  return protocol_version();
}
