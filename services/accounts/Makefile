# PlayGlobalChess accounts service (X-3, Go) — build + test.
#
# Unlike game-server, accounts is PURE Go (no cgo / Rust staticlibs): the move
# hot path and the rules engine do not live here. So there is no `make lib`
# step — just build, vet, and test.

.PHONY: build test vet clean

build:
	go build ./...

vet:
	go vet ./...

test:
	go test ./...

clean:
	go clean ./...
