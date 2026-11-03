# Smif: local checks (no GitHub Actions in this workspace; see docs/adr/0001).
GO ?= go
BIN ?= bin/smif
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/appnice/smif/internal/daemon.Version=$(VERSION)
GOLANG_IMAGE ?= golang:1.27

.PHONY: bench bench-real build test race e2e check check-linux fmt-check lint cover install-local smoke-real clean

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/smif

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

e2e:
	$(GO) test -count=1 ./e2e/...

# fmt-check fails when gofmt would change any .go file (testdata included).
GOFMT ?= gofmt
fmt-check:
	@out="$$($(GOFMT) -l .)"; if [ -n "$$out" ]; then echo "gofmt needed (run: $(GOFMT) -w <file>):"; echo "$$out"; exit 1; fi

lint:
	$(GO) vet ./...
	staticcheck ./...

cover:
	$(GO) test -coverprofile=cover.out ./internal/router/ ./internal/scheduler/ ./internal/store/ ./internal/runner/claude/ ./e2e/ \
	  -coverpkg=./internal/router/,./internal/scheduler/,./internal/store/,./internal/runner/claude/
	$(GO) tool cover -func=cover.out | tail -1

check: fmt-check lint race build
	./$(BIN) config validate --config testdata/rfc-example.toml
	! ./$(BIN) config validate --config testdata/no-catchall.toml 2>/dev/null

# check-linux runs make check in a Linux container. The repo mounts read-only and the
# container works on a copy in /tmp/src, so the host bin/smif stays a macOS binary.
# No git config: a worktree .git points to a host path the container cannot see.
# With a host module cache, the container has no network: the cache mounts read-only as
# GOPROXY, and the host sumdb cache lets go verify checksums offline.
STATICCHECK_VERSION ?= v0.8.2
HOST_GOMODCACHE := $(shell $(GO) env GOMODCACHE 2>/dev/null)
HOST_GOSUMDB_DIR := $(firstword $(subst :, ,$(shell $(GO) env GOPATH 2>/dev/null)))/pkg/sumdb
ifneq ($(and $(wildcard $(HOST_GOMODCACHE)/cache/download/sumdb),$(wildcard $(HOST_GOSUMDB_DIR))),)
CHECK_LINUX_MODS := --network none -e GOPROXY=file:///hostmods \
	  -v $(HOST_GOMODCACHE)/cache/download:/hostmods:ro \
	  -v $(HOST_GOMODCACHE)/cache/download/sumdb:/go/pkg/mod/cache/download/sumdb:ro \
	  -v $(HOST_GOSUMDB_DIR):/go/pkg/sumdb:ro
endif

check-linux:
	docker run --rm -v $(CURDIR):/src:ro $(CHECK_LINUX_MODS) -w / \
	  -e GOFLAGS=-buildvcs=false -e GOTOOLCHAIN=local \
	  -e GIT_AUTHOR_NAME=ci -e GIT_AUTHOR_EMAIL=ci@local -e GIT_COMMITTER_NAME=ci -e GIT_COMMITTER_EMAIL=ci@local \
	  $(GOLANG_IMAGE) sh -ec 'cp -R /src /tmp/src && rm -rf /tmp/src/bin && cd /tmp/src && \
	  go install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) && \
	  PATH=$$PATH:$$(go env GOPATH)/bin make check'

install-local: build
	install -m 0755 $(BIN) $(HOME)/.local/bin/smif

bench: ## benchmark tiers S, R and T on the fake claude ($0); report in bench/results/
	go run ./bench

bench-real: ## bench plus actual tokens on haiku through the existing claude login (no API key)
	go run ./bench -real

smoke-real:
	SMIF_E2E_REAL=1 $(GO) test -count=1 -run 'TestRealClaudeSmoke|TestRealWorkerConfinement' -v ./e2e/

clean:
	rm -rf bin cover.out dist

# v2.18.3 is the newest GoReleaser release whose go.mod accepts the go 1.27.2 toolchain of go.mod
# and the host (v2.19.0 needs go >= 1.27.3 and a silent GOTOOLCHAIN=auto download). ADR-0001.
GORELEASER_VERSION ?= v2.18.3
GORELEASER ?= go run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)

release-check:
	$(GORELEASER) check

release-snapshot: check
	$(GORELEASER) release --snapshot --clean
