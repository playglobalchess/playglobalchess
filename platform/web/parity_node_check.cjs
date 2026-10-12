// Host-provable web codec parity check (SPEC-S2-PROTOCOL §8, PLAN-CODEC §4.4,
// NFR-10). Loads the SHIPPED wasm artifact (protocol/codec-wasm/pkg-node,
// produced by `wasm-pack build --target nodejs`) and asserts each committed
// golden frame decodes→re-encodes byte-identically through the wasm codec, and
// that a malformed frame throws rather than trapping.
//
// This is the companion to protocol/codec-wasm/tests/web_parity.rs: that test is
// the canonical CI form (`wasm-pack test --node`); this script proves the same
// thing against the built package and runs anywhere node + the pkg exist (the
// wasm-bindgen test runner's realpath walk is blocked in some sandboxes — this
// is not). Frames mirror protocol/codec-ffi/tests/codec_parity_oracle.rs; change
// in lockstep (the parity contract).
//
// Run via platform/web/build_codec_web.sh (which builds pkg-node first).

const path = require("path");
const pkg = path.join(__dirname, "..", "..", "protocol", "codec-wasm", "pkg-node", "pgc_protocol_wasm.js");
const w = require(pkg);

const hex = (s) => Uint8Array.from(s.match(/../g).map((b) => parseInt(b, 16)));
const eq = (a, b) => a.length === b.length && a.every((x, i) => x === b[i]);

const VECTORS = {
  client: "01032466343761633130622d353863632d343337322d613536372d30653032623263336434373967056537653871",
  server: "01852466343761633130622d353863632d343337322d613536372d3065303262326333643437390202000a021817",
  event: "2967616d652f66343761633130622d353863632d343337322d613536372d3065303262326333643437390c0c008080cea6a5320e626c69747a2d7374616e646172640200b817d0173c8fc2753d01b817a0173c8fc2753d",
};

let fail = 0;
const check = (ok, msg) => {
  console.log((ok ? "OK  " : "FAIL ") + msg);
  if (!ok) fail++;
};

for (const [k, fn] of [
  ["client", w.reencode_client],
  ["server", w.reencode_server],
  ["event", w.reencode_event],
]) {
  const b = hex(VECTORS[k]);
  check(eq(fn(b), b), `${k} decode→re-encode byte-identical`);
}

let threw = false;
try {
  w.reencode_client(new Uint8Array([]));
} catch (_e) {
  threw = true;
}
check(threw, "empty frame throws (no wasm trap)");
check(w.protocol_version() === 1, "protocol_version() === 1");

if (fail) {
  console.error(`\nWeb codec parity: ${fail} FAILED`);
  process.exit(1);
}
console.log("\nWeb codec parity: all checks pass");
