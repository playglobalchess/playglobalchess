//! Golden-vector generator. Writes `protocol/vectors/vectors.json` from the
//! shared canonical corpus (`tests/corpus/mod.rs`). Run from the crate root:
//!
//! ```text
//! cargo run --example gen_vectors
//! ```
//!
//! This is the ONLY sanctioned way to (re)generate the committed vectors. The
//! committed JSON is the frozen cross-language oracle (SPEC-S2-PROTOCOL §8); the
//! `golden_freeze` test asserts the live codec still reproduces every byte, so
//! regenerating after an *intended* wire change is a deliberate, reviewed act —
//! and an *accidental* field reorder shows up as a failing freeze test, not a
//! silent regeneration.

#[path = "../tests/corpus/mod.rs"]
mod corpus;

use std::fmt::Write as _;
use std::fs;
use std::path::Path;

fn to_hex(bytes: &[u8]) -> String {
    let mut s = String::with_capacity(bytes.len() * 2);
    for b in bytes {
        let _ = write!(s, "{b:02x}");
    }
    s
}

fn main() {
    let items = corpus::corpus();

    // Build the JSON by hand (no serde needed for the writer side) so the file
    // format is obvious and stable. Schema: { "schema": 1, "vectors": [ {name,
    // kind, hex, bytes} ] }.
    let mut out = String::new();
    out.push_str("{\n");
    out.push_str("  \"schema\": 1,\n");
    out.push_str(
        "  \"note\": \"Frozen golden vectors for pgc-protocol (SPEC-S2-PROTOCOL \\u00a74A). \
Each entry: name, kind (client|server|event), hex (the exact encoded frame bytes), byte count. \
Regenerate ONLY via `cargo run --example gen_vectors`; the golden_freeze test guards these.\",\n",
    );
    out.push_str("  \"vectors\": [\n");

    for (i, (name, item)) in items.iter().enumerate() {
        let kind = match item {
            corpus::Item::Client(_) => "client",
            corpus::Item::Server(_) => "server",
            corpus::Item::Event(_) => "event",
        };
        let bytes = corpus::encode_item(item);
        let comma = if i + 1 == items.len() { "" } else { "," };
        let _ = writeln!(
            out,
            "    {{ \"name\": \"{}\", \"kind\": \"{}\", \"bytes\": {}, \"hex\": \"{}\" }}{}",
            name,
            kind,
            bytes.len(),
            to_hex(&bytes),
            comma
        );
    }
    out.push_str("  ]\n}\n");

    // protocol/vectors/ is a sibling of protocol/codec/ (this crate root).
    let dest = Path::new(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .expect("codec crate has a parent (protocol/)")
        .join("vectors")
        .join("vectors.json");
    fs::create_dir_all(dest.parent().unwrap()).expect("create protocol/vectors/");
    fs::write(&dest, out).expect("write vectors.json");
    println!("wrote {} vectors to {}", items.len(), dest.display());
}
