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
  },
  build: {
    target: "es2022",
  },
});
