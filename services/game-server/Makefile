# PlayGlobalChess game-server (X-2, Go, ADR-12) — build + test.
#
# The move/event hot path links the pgc-protocol codec in-process via cgo
# (ADR-14/ADR-15, PLAN-S2-CODEC v1.1 §4.5). `make lib` builds the Rust
# staticlib and stages the .a + C header into internal/codec/lib/ (a gitignored
# build output the cgo directives in internal/codec/codec.go resolve against).

CODEC_CGO_DIR := ../../protocol/codec-cgo
LIB_DST       := internal/codec/lib
# RUST_TARGET lets CI/deploy cross-compile per platform (linux/amd64 ->
# x86_64-unknown-linux-gnu, linux/arm64 -> aarch64-unknown-linux-gnu). Empty =
# host target (local dev / this macOS box).
RUST_TARGET ?=
CARGO_FLAGS := --release --manifest-path $(CODEC_CGO_DIR)/Cargo.toml
ifneq ($(RUST_TARGET),)
CARGO_FLAGS += --target $(RUST_TARGET)
RUST_OUT    := $(CODEC_CGO_DIR)/target/$(RUST_TARGET)/release
else
RUST_OUT    := $(CODEC_CGO_DIR)/target/release
endif

.PHONY: lib test clean

lib:
	cargo build $(CARGO_FLAGS)
	mkdir -p $(LIB_DST)
	cp $(RUST_OUT)/libpgc_protocol_cgo.a $(LIB_DST)/
	# The C header is NOT copied — cgo includes it from the canonical crate dir
	# (protocol/codec-cgo/include) via CFLAGS, so there is no copy to drift.

test: lib
	go test ./...

clean:
	rm -rf $(LIB_DST)
	go clean ./...
