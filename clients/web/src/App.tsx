import { t } from "./i18n";
import { codecProtocolVersion } from "./codec/seam";

// Step-1 shell: proves the app builds, typechecks, and that the ONE shared §4A
// codec (pgc-protocol-wasm) is wired and callable from TS. The real F-01 UI
// (GameSocket, board, reducer) lands in the following steps of this slice.
export function App(): React.JSX.Element {
  return (
    <main>
      <h1>{t("app.title")}</h1>
      <p>{t("app.tagline")}</p>
      <p aria-label={t("codec.version.aria")}>
        {t("codec.version.label")} <code>{codecProtocolVersion()}</code>
      </p>
    </main>
  );
}
