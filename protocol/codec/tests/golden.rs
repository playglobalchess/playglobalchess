//! Round-trip and GOLDEN-FREEZE tests over the full canonical corpus
//! (SPEC-S2-PROTOCOL §8, PLAN-CODEC §4.2/§5).
//!
//! - Round-trip: `decode(encode(x)) == x` for every message and event type.
//! - Golden freeze: the live codec reproduces every committed vector in
//!   `protocol/vectors/vectors.json` **byte-for-byte** — the guard that makes an
//!   accidental §4A field reorder a loud failure instead of a silent wire break.

mod corpus;

use corpus::{encode_item, Item};
use std::collections::BTreeMap;

use pgc_protocol::{decode_client, decode_event, decode_server};

/// `decode(encode(x)) == x` for every corpus item, via the direction-correct
/// codec entry point.
#[test]
fn round_trip_every_type() {
    for (name, item) in corpus::corpus() {
        let bytes = encode_item(&item);
        match item {
            Item::Client(ref m) => {
                let back = decode_client(&bytes)
                    .unwrap_or_else(|e| panic!("{name}: client decode failed: {e}"));
                assert_eq!(&back, m, "{name}: client round-trip mismatch");
            }
            Item::Server(ref m) => {
                let back = decode_server(&bytes)
                    .unwrap_or_else(|e| panic!("{name}: server decode failed: {e}"));
                assert_eq!(&back, m, "{name}: server round-trip mismatch");
            }
            Item::Event(ref e) => {
                let back = decode_event(&bytes)
                    .unwrap_or_else(|err| panic!("{name}: event decode failed: {err}"));
                assert_eq!(&back, e, "{name}: event round-trip mismatch");
            }
        }
    }
}

/// Parse the committed `vectors.json` into `name -> (kind, hex)`. Hand-rolled
/// against the simple fixed schema (serde_json is a dev-dep but the file shape
/// is trivial and stable), tolerant of whitespace.
fn load_committed_vectors() -> BTreeMap<String, (String, String)> {
    let path = concat!(env!("CARGO_MANIFEST_DIR"), "/../vectors/vectors.json");
    let text = std::fs::read_to_string(path)
        .unwrap_or_else(|e| panic!("read committed vectors at {path}: {e}"));
    let doc: serde_json::Value = serde_json::from_str(&text).expect("vectors.json is valid JSON");
    let arr = doc
        .get("vectors")
        .and_then(|v| v.as_array())
        .expect("vectors.json has a `vectors` array");
    let mut out = BTreeMap::new();
    for v in arr {
        let name = v["name"].as_str().expect("vector name").to_string();
        let kind = v["kind"].as_str().expect("vector kind").to_string();
        let hex = v["hex"].as_str().expect("vector hex").to_string();
        out.insert(name, (kind, hex));
    }
    out
}

fn to_hex(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

/// GOLDEN FREEZE. The live codec must reproduce every committed vector's bytes
/// exactly. A failure here means the wire format changed — an intentional change
/// is applied by regenerating (`cargo run --example gen_vectors`) under
/// change-control; an accidental one (a reordered §4A field) is the bug this
/// test exists to catch.
#[test]
fn golden_freeze_live_codec_matches_committed_bytes() {
    let committed = load_committed_vectors();
    let live = corpus::corpus();

    // 1. Every live corpus item has a committed vector with identical bytes.
    for (name, item) in &live {
        let (kind, hex) = committed.get(*name).unwrap_or_else(|| {
            panic!("corpus item `{name}` has no committed vector — run `cargo run --example gen_vectors` and review the diff")
        });
        let expected_kind = match item {
            Item::Client(_) => "client",
            Item::Server(_) => "server",
            Item::Event(_) => "event",
        };
        assert_eq!(kind, expected_kind, "{name}: committed kind differs");
        let live_hex = to_hex(&encode_item(item));
        assert_eq!(
            &live_hex, hex,
            "{name}: GOLDEN FREEZE BROKEN — live codec bytes differ from the committed vector. \
If this change is intentional, regenerate the vectors under change-control; otherwise a §4A \
field order/type was altered."
        );
    }

    // 2. No committed vector is orphaned (every frozen vector still has a live
    //    source) — catches a type being dropped from the corpus.
    let live_names: std::collections::BTreeSet<&str> = live.iter().map(|(n, _)| *n).collect();
    for name in committed.keys() {
        assert!(
            live_names.contains(name.as_str()),
            "committed vector `{name}` has no live corpus source — a type was removed; \
regenerate under change-control if intentional"
        );
    }
}

/// Every committed vector also decodes cleanly through the live codec (not just
/// re-encodes) — the cross-direction half of the freeze.
#[test]
fn committed_vectors_decode() {
    fn from_hex(h: &str) -> Vec<u8> {
        (0..h.len())
            .step_by(2)
            .map(|i| u8::from_str_radix(&h[i..i + 2], 16).expect("hex pair"))
            .collect()
    }
    for (name, (kind, hex)) in load_committed_vectors() {
        let bytes = from_hex(&hex);
        match kind.as_str() {
            "client" => {
                decode_client(&bytes).unwrap_or_else(|e| {
                    panic!("{name}: committed client vector failed decode: {e}")
                });
            }
            "server" => {
                decode_server(&bytes).unwrap_or_else(|e| {
                    panic!("{name}: committed server vector failed decode: {e}")
                });
            }
            "event" => {
                decode_event(&bytes).unwrap_or_else(|e| {
                    panic!("{name}: committed event vector failed decode: {e}")
                });
            }
            other => panic!("{name}: unknown kind `{other}`"),
        }
    }
}
