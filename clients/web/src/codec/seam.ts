// The TS binding to the ONE shared §4A codec (pgc-protocol-wasm, compiled from
// the pure Rust codec — NFR-5/NFR-10). This module is the ONLY place the web
// client touches the wasm seam; the rest of the app speaks typed ClientMsg /
// ServerMsg (see ./messages).
//
// The seam produces/consumes §4A FRAME bytes entirely in Rust — this layer only
// carries the typed object across the boundary and narrows the seam's `any` to
// our mirror types ONCE, here. The shapes are regression-locked by the S4a
// node-parity + hand-built golden tests on the Rust side, so a drift between
// these TS types and the wire is caught there (and at runtime: the seam THROWS
// on a shape it cannot (de)serialize).

import {
  encodeClient as wasmEncodeClient,
  decodeServer as wasmDecodeServer,
  encodeServer as wasmEncodeServer,
  decodeClient as wasmDecodeClient,
  protocol_version,
} from "pgc-protocol-wasm";
import type { ClientMsg, ServerMsg } from "./messages";

/** The §4A protocol major version the shared codec speaks (§4A.1). */
export function codecProtocolVersion(): number {
  return protocol_version();
}

/** Build a §4A client→server frame from a typed ClientMsg. Throws on a bad shape. */
export function encode(msg: ClientMsg): Uint8Array {
  return wasmEncodeClient(msg);
}

/**
 * Read a §4A server→client frame into a typed ServerMsg. Throws if the frame is
 * not a valid §4A server frame (the caller — the read loop — catches and
 * surfaces it rather than letting it crash the socket).
 */
export function decode(frame: Uint8Array): ServerMsg {
  // The seam returns `any`; it is a valid ServerMsg shape by construction when
  // decode_server succeeds (the Rust codec produced it). Narrow once, here.
  return wasmDecodeServer(frame) as ServerMsg;
}

// --- test mirrors (not used by the client at runtime; used by the fake-socket
// test harness to BUILD server frames and READ client frames, exactly as the
// GS-6 e2e used the Go seam's client mirror) --------------------------------

/** TEST ONLY: build a §4A server→client frame from a typed ServerMsg. */
export function encodeServerFrame(msg: ServerMsg): Uint8Array {
  return wasmEncodeServer(msg);
}

/** TEST ONLY: read a §4A client→server frame into a typed ClientMsg. */
export function decodeClientFrame(frame: Uint8Array): ClientMsg {
  return wasmDecodeClient(frame) as ClientMsg;
}
