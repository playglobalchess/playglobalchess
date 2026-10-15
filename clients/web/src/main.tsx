import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";

const rootEl = document.getElementById("root");
if (rootEl === null) {
  throw new Error("root element #root not found");
}
createRoot(rootEl).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
