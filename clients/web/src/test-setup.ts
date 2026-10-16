// Vitest setup: tell React this is an act() testing environment so state
// updates flushed inside act(...) are handled synchronously and React does not
// warn. Applies to the jsdom hook/component tests; harmless for node tests.
// See: https://react.dev/reference/react/act
(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;
