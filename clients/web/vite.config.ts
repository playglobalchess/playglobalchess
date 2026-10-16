import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import wasm from "vite-plugin-wasm";

// The web client binds the one shared §4A codec (pgc-protocol-wasm) compiled to
// wasm — vite-plugin-wasm lets us import the wasm-pack ESM bundle directly
// (NFR-5: the codec is Rust-produced, never re-implemented in TS).
export default defineConfig({
  plugins: [react(), wasm()],
  server: {
    // Dev server binds loopback only (never 0.0.0.0) — the control plane rule.
    host: "127.0.0.1",
    port: 5173,
    // Proxy the §4A WebSocket to the local game server (industry-standard dev
    // setup — browser stays same-origin on the Vite port). ws:true forwards the
    // upgrade. We do NOT set changeOrigin: the server allows the loopback dev
    // Origin (127.0.0.1:5173) via OriginPatterns, so the browser's real Origin
    // is forwarded unchanged — rewriting it has caused frame-flow breakage.
    proxy: {
      "/ws": {
        target: process.env.PGC_WS_TARGET ?? "ws://127.0.0.1:7733",
        ws: true,
      },
    },
  },
  build: {
    target: "es2022",
  },
});
