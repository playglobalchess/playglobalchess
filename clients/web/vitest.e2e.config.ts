import { defineConfig } from "vitest/config";
import wasm from "vite-plugin-wasm";

// Separate config for the real-server end-to-end test: it spawns the Go
// e2e-server binary and talks to it over a real WebSocket, so it is NOT part of
// the fast unit suite (vitest.config.ts). Run with `npm run e2e`, which builds
// the server binary first. Single-threaded + a generous timeout because a run
// boots a subprocess and plays a networked game.
export default defineConfig({
  plugins: [wasm()],
  test: {
    environment: "node",
    include: ["e2e/**/*.e2e.test.ts"],
    testTimeout: 20_000,
    hookTimeout: 20_000,
    fileParallelism: false,
  },
});
