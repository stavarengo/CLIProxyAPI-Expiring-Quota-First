# Develop

How to test Expiring Quota First, reproduce a release build, build for another platform, and release.

The tests need the Go version in `go.mod` and a C compiler:

```bash
go vet ./... && go test -race ./...
```

`./build.sh` reproduces the release build inside Docker and checks the result with CLIProxyAPI's own plugin store code; `./build.sh --help` says what it writes where.
For another platform, build on it with cgo enabled:

```bash
go build -trimpath -buildmode=c-shared -ldflags "-X main.version=<version>" -o expiring-quota-first.so .
```

The repository has one workflow, Release, and only a pushed tag `v<version>` runs it. It builds, publishes the GitHub release, and then installs the published release with that same plugin store code.
Issues and pull requests are welcome.
