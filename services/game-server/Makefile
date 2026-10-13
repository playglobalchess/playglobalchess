# PlayGlobalChess game-server (X-2, Go, ADR-12) — build + test.
#
# The move/event hot path links two Rust staticlibs in-process via cgo
# (ADR-14/ADR-15): the pgc-protocol codec (PLAN-S2-CODEC v1.1 §4.5) and the
# pgc-engine rules engine (SPEC-S2-GAME-SERVER-GS2). `make lib` builds both and
# stages each .a into its package's lib/ dir (gitignored build outputs the cgo
# directives resolve against). C headers are NOT copied — cgo includes them from
# the canonical crate dirs via CFLAGS, so there is nothing to drift.

CODEC_CGO_DIR  := ../../protocol/codec-cgo
CODEC_LIB_DST  := internal/codec/lib
ENGINE_CGO_DIR := ../../engine/engine-cgo
ENGINE_LIB_DST := internal/engine/lib
# RUST_TARGET lets CI/deploy cross-compile per platform (linux/amd64 ->
# x86_64-unknown-linux-gnu, linux/arm64 -> aarch64-unknown-linux-gnu). Empty =
# host target (local dev / this macOS box).
RUST_TARGET ?=
ifneq ($(RUST_TARGET),)
TARGET_FLAG := --target $(RUST_TARGET)
CODEC_OUT   := $(CODEC_CGO_DIR)/target/$(RUST_TARGET)/release
ENGINE_OUT  := $(ENGINE_CGO_DIR)/target/$(RUST_TARGET)/release
else
TARGET_FLAG :=
CODEC_OUT   := $(CODEC_CGO_DIR)/target/release
ENGINE_OUT  := $(ENGINE_CGO_DIR)/target/release
endif

.PHONY: lib test clean

lib:
	cargo build --release --manifest-path $(CODEC_CGO_DIR)/Cargo.toml $(TARGET_FLAG)
	mkdir -p $(CODEC_LIB_DST)
	cp $(CODEC_OUT)/libpgc_protocol_cgo.a $(CODEC_LIB_DST)/
	cargo build --release --manifest-path $(ENGINE_CGO_DIR)/Cargo.toml $(TARGET_FLAG)
	mkdir -p $(ENGINE_LIB_DST)
	cp $(ENGINE_OUT)/libpgc_engine_cgo.a $(ENGINE_LIB_DST)/
	# Headers are NOT copied — cgo includes them from the canonical crate
	# include/ dirs via CFLAGS, so there is no copy to drift.

test: lib
	go test ./...

clean:
	rm -rf $(CODEC_LIB_DST) $(ENGINE_LIB_DST)
	go clean ./...
