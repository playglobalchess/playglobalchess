import js from "@eslint/js";
import tseslint from "@typescript-eslint/eslint-plugin";
import tsparser from "@typescript-eslint/parser";
import reactHooks from "eslint-plugin-react-hooks";

// Browser + ES globals for app code; Node globals for the build scripts. We do
// NOT rely on eslint's core `no-undef` for TS/TSX — TypeScript itself resolves
// identifiers and the lib config (DOM, ES2022) is the source of truth, so core
// no-undef only produces false positives on globals it doesn't know. It is
// disabled on TS files (the typescript-eslint recommended guidance).
const browserGlobals = {
  window: "readonly",
  document: "readonly",
  console: "readonly",
  WebSocket: "readonly",
  URL: "readonly",
  BigInt: "readonly",
  setTimeout: "readonly",
  clearTimeout: "readonly",
};

const nodeGlobals = {
  process: "readonly",
  console: "readonly",
  URL: "readonly",
};

export default [
  { ignores: ["dist", "node_modules", "coverage"] },
  js.configs.recommended,
  {
    files: ["src/**/*.{ts,tsx}", "vite.config.ts"],
    languageOptions: {
      parser: tsparser,
      parserOptions: { project: "./tsconfig.json" },
      ecmaVersion: 2022,
      sourceType: "module",
      globals: browserGlobals,
    },
    plugins: {
      "@typescript-eslint": tseslint,
      "react-hooks": reactHooks,
    },
    rules: {
      ...tseslint.configs.recommended.rules,
      ...reactHooks.configs.recommended.rules,
      // TS resolves identifiers; core no-undef only false-positives here.
      "no-undef": "off",
      // NFR-5-adjacent discipline: no `any` escapes (use unknown + narrow).
      "@typescript-eslint/no-explicit-any": "error",
      "@typescript-eslint/no-non-null-assertion": "error",
    },
  },
  {
    files: ["scripts/**/*.mjs"],
    languageOptions: {
      ecmaVersion: 2022,
      sourceType: "module",
      globals: nodeGlobals,
    },
  },
];
