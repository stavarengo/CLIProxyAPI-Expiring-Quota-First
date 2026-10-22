# Develop

How to test Expiring Quota First, reproduce a release build, build for another platform, and release.

The tests need the Go version in `go.mod` and a C compiler:

```bash
go vet ./... && go test -race ./...
```

`./build.sh` reproduces the Linux release build inside Docker and checks the result with CLIProxyAPI's own plugin store code; `./build.sh --help` says what it writes where.
The macOS and Windows libraries are built only by the Release workflow, on GitHub's runners for those systems, with `scripts/build-library.sh`, which also builds the patched toolchain an Intel Mac needs ([Install](install.md#platforms) says why); `scripts/package-release.sh` then zips and checks every platform. Each script's `--help` says what it takes.
For another platform, build on it with cgo enabled:

```bash
go build -trimpath -buildmode=c-shared -ldflags "-X main.version=<version>" -o expiring-quota-first.so .
```

The repository has one workflow, Release. A pushed tag `v<version>` runs it fully: it builds every platform, publishes the GitHub release, and then installs the published release with that same plugin store code. Running it from the Actions tab does everything but publish, as version 0.0.0-dev.
Issues and pull requests are welcome.
