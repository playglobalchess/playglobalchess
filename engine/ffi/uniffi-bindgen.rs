//! Binding generator entrypoint for pgc-engine-ffi (UniFFI 0.32).
//! Run: `cargo run --bin uniffi-bindgen -- generate --library <dylib> --language swift|kotlin --out-dir <dir>`.
fn main() {
    uniffi::uniffi_bindgen_main()
}
