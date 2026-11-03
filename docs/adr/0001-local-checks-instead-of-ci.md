# ADR-0001: Local checks instead of hosted CI

**Status:** accepted, 2026-11-02

**What.** RFC M0 asks for "CI on macOS and Linux". This repository has no hosted CI. `make check` runs vet,
staticcheck, the race-enabled test suite (including e2e with a `-race` daemon), the build and the config
validation gates on the Mac; `make check-linux` runs the same target in a `golang:1.27` Docker container.

**Why.** The Appnice workspace switched GitHub Actions off on every repository on 2026-10-14 and forbids
`.github/workflows/`. Checks run locally before a commit or release.

**Trade-off.** Nobody runs the checks automatically on push. The release step (`make release`) depends on
`check`, so a release cannot skip them.

**Release tool pin (2026-11-02).** The Makefile pins GoReleaser to an exact version (`GORELEASER_VERSION`,
v2.18.3), never `@latest`. The pin must declare a `go` version that the go.mod toolchain (go 1.27.2) accepts,
so `GOTOOLCHAIN=local make release-check` works with no toolchain download. v2.19.0 declares go 1.27.3: with
the default `GOTOOLCHAIN=auto` it downloads go1.27.3 without notice, and with `GOTOOLCHAIN=local` it fails.
Offline, set `GOPROXY=file://$(go env GOMODCACHE)/cache/download`: with `GOPROXY=off`, `go run` fails on its
deprecation lookup. Move to a newer GoReleaser together with the go.mod toolchain bump, not before.
