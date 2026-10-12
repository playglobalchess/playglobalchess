//! The single typed error surface for the codec. Every malformed input on the
//! decode path returns a `CodecError` — the codec **never panics** on decode
//! (PLAN-CODEC §4.1: a panic across the FFI/wasm/cgo boundary is a client hard
//! crash). `thiserror` per engineering-conventions.md.

use thiserror::Error;

/// SPEC-S2-PROTOCOL §4A.1 maximum frame size (v1). An encoded or decoded frame
/// larger than this is a protocol violation.
pub const MAX_FRAME_BYTES: usize = 4096;

/// Errors produced on the decode path (and the two defensive encode-side
/// checks). Each variant maps to a SPEC-S2-PROTOCOL §4A / §5 failure mode so a
/// caller (the game-server) can translate it into the right `S ERROR {code}` /
/// `MOVE_REJECTED {reason}` wire response.
///
/// Decode is total over all byte inputs: it returns one of these, never panics,
/// never silently accepts a malformed frame (SPEC-S2-PROTOCOL §8 negative
/// decode).
#[derive(Debug, Error, PartialEq, Eq)]
pub enum CodecError {
    /// Frame shorter than the 2-byte `ver | type` header (§4A.1). Truncated.
    #[error("frame too short: need at least 2 header bytes, got {0}")]
    TooShort(usize),

    /// `ver` byte is not a protocol version this codec supports. v1 = 0x01.
    /// Maps to `ERROR {code=UNSUPPORTED_VERSION}` (§4A.7, §5).
    #[error("unsupported protocol version: 0x{0:02x}")]
    UnsupportedVersion(u8),

    /// `type` byte is not a known message/event type code for this direction.
    /// Maps to `ERROR {code=PROTOCOL_VIOLATION}` (§4A.7, §5 malformed frame).
    #[error("unknown type code: 0x{0:02x}")]
    UnknownType(u8),

    /// The type code is a valid code but for the *wrong direction* on this
    /// decode path (e.g. a server type arriving at the client decoder, or a
    /// client type at the server decoder). High-bit direction check (§4A.2).
    /// Maps to `ERROR {code=PROTOCOL_VIOLATION}` (§3.1, §5).
    #[error("wrong-direction type code 0x{0:02x} for the {1} decode path")]
    WrongDirection(u8, &'static str),

    /// Frame exceeds the §4A.1 4 KB cap (checked against actual postcard output,
    /// PLAN-CODEC §3). Maps to `ERROR {code=PROTOCOL_VIOLATION}` (§4A.1, §5).
    #[error("frame too large: {0} bytes exceeds the {MAX_FRAME_BYTES}-byte cap")]
    TooLarge(usize),

    /// The payload bytes could not be deserialized by postcard: truncated,
    /// trailing garbage, a bad varint, or a value out of range for its field
    /// (e.g. a bad enum discriminant). Maps to `ERROR {code=PROTOCOL_VIOLATION}`.
    #[error("malformed payload: {0}")]
    MalformedPayload(String),
}
