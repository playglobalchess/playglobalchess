// Command accounts is the entrypoint for the X-3 accounts & identity service
// (spec SPEC-S3-ACCOUNTS). This is a scaffold: the HTTP transport, store, and
// auth mechanisms are added in later chunks. For now it starts, logs, and exits
// cleanly so the build/CI gate has a real binary to compile.
package main

import "log"

func main() {
	log.Println("accounts service (X-3) — scaffold; transport/store not wired yet")
}
