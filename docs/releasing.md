# Releasing

Releases are built on a maintainer's machine; there is no CI (ADR-0001). GitHub keeps the code, the tags and
the release files.

1. `make check` passes on a clean `main`, and `make check-linux` passes (Docker).
2. Pick the version (semver) and tag it: `git tag -a v0.2.0 -m v0.2.0`.
3. Build: `make release-snapshot` checks the build; for the release itself run
   `GOTOOLCHAIN=local go run github.com/goreleaser/goreleaser/v2@v2.18.3 release --clean --skip=publish`.
   `dist/` then holds `mailroom_<os>_<arch>.tar.gz` for darwin and linux on amd64 and arm64, and
   `checksums.txt`.
4. Smoke test the binary for this machine: `tar -xzf dist/mailroom_darwin_arm64.tar.gz mailroom && ./mailroom version`.
5. Push the tag and publish the files:
   `git push origin v0.2.0 && gh release create v0.2.0 dist/*.tar.gz dist/checksums.txt --title v0.2.0 --notes-file <notes>`.
   The archive names carry no version, so `releases/latest/download/mailroom_<os>_<arch>.tar.gz` in the README
   always gets the newest release.
6. `go install github.com/appnice/mailroom/cmd/mailroom@v0.2.0` works once the tag is public.

Before the first public release, also check that nothing private is in the tree or its history (a secret
scan over `git log -p`), and decide whether the public repository starts from a fresh history.
