# SAK Media Server build helpers.
#
# IMPORTANT: `make aria2c` MUST run before `go build ./...` (or `go build
# ./cmd/sakms`). The server embeds a static aria2c binary via
# `//go:embed assets/aria2c` (internal/downloader/embed.go); that file is a
# multi-MB platform artifact that is NOT committed to git, so a fresh checkout
# fails to build with `pattern assets/aria2c: no matching files found` until
# this target fetches it — the same generated-artifact contract as the
# frontend bundle under internal/web/static. The Dockerfile runs this in a
# build stage before compiling.

.PHONY: aria2c
aria2c:
	go run ./cmd/download-aria2c

.PHONY: dto
dto:
	go run ./cmd/gendto

.PHONY: build
build: aria2c
	CGO_ENABLED=0 go build ./...
