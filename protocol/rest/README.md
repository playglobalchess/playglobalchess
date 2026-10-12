# protocol/rest — the frozen v1 REST contract (machine-readable)

`openapi.yaml` is the **executable projection** of SPEC-PE-API-CONTRACT (RATIFIED)
— the v1 REST/JSON surface (X-13), F-01 vertical slice. It is to the REST half of
**G-D** what `protocol/vectors/` is to the realtime half: the frozen contract made
machine-checkable, so every future server and client validates against one source
of truth (NFR-10).

## What it is / is NOT

- **Derived, not authored.** Every path, schema field, and error `type` comes
  verbatim from the spec's Appendix A (schemas), Appendix B (error registry) and
  §3.1 conventions. It adds nothing. The **spec is the source of truth**; this
  file must track it, and `platform/ci/check_rest_contract.py` fails on any drift.
- **The REST surface only.** The realtime surface is WebSocket (SPEC-S2-PROTOCOL)
  and is deliberately absent here — the §3.3 boundary. The audit asserts no live
  WS verb (move/seek/resign/…) ever appears as a REST path.
- **The F-01 slice.** Only endpoints with frozen Appendix A schemas. Tournaments
  (`/v1/tournaments`) and clubs (`/v1/clubs`) are §3.2 catalog placeholders without
  schemas and are intentionally absent until F-06/F-23 add them as `/v1` additive
  changes.

## The audit (R1 — the static, server-free half of §8)

`platform/ci/check_rest_contract.py` (stdlib-only, like `check_docs.py`) runs on
every change and asserts:
1. `openapi.yaml` is a valid OpenAPI 3.1 document;
2. **boundary (§3.3):** no WS operation (SPEC-S2-PROTOCOL §4A.2) is exposed as a
   REST path, and no live-game verb leaks into REST;
3. **catalog parity:** the frozen §3.2/Appendix-A path set == the openapi.yaml path
   set (no drift either way);
4. **error parity:** the Appendix B `type` registry == the openapi.yaml
   `Problem.type` enum (closed v1 registry).

```sh
python3 platform/ci/check_rest_contract.py          # the gate
npx @redocly/cli lint protocol/rest/openapi.yaml     # optional: structural OpenAPI lint
```

## R1 vs R2 (what this does NOT yet do)

This is **R1**: the frozen contract artifact + the static audits that need no
running server. The **executable conformance suite** (§8: request/response +
convention + negative/abuse tests run against a real REST server and the three
clients, NFR-10) is **R2** — it is gated on P1 implementation work (a REST server
+ a client exist to test), so it ships with the F-01 vertical slice, not here.

Regenerating: `openapi.yaml` is edited by hand to track the spec; the audit is the
guard that it stays faithful. A spec change (always a reviewed `/v1`-additive or a
`/v2` version bump — never in-place after freeze) updates both together.
