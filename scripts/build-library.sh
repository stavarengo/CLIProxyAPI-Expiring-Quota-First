#!/usr/bin/env bash

((BASH_VERSINFO[0] * 10000 + BASH_VERSINFO[1] * 100 + BASH_VERSINFO[2] >= 50215)) || {
  printf 'Bash >= 5.2.15 required, found %s\n' "${BASH_VERSION}" >&2
  exit 1
}

set -o errtrace
set -o errexit
set -o nounset
set -o pipefail
shopt -s inherit_errexit

# The Go release the darwin/amd64 toolchain is rebuilt from. Keep it equal to the Go version in build.sh and .github/workflows/ci.yml.
readonly GO_VERSION="1.27.3"
readonly GO_SOURCE_SHA256="6e7749035cf360f06b2575d162ea79df9fda8ef671889e5c375f8e50777f26bb"
# Go 1.27 supports macOS 13 and later; without a floor the library would demand the macOS of the build machine.
readonly MACOS_MIN_VERSION="13.0"

printHelp()
{
  cat <<EOF
Usage: ${0##*/} --version <version> --platform <goos/goarch> --out <file>

Builds the plugin library for one platform with the go on PATH, cgo enabled, and removes the C header Go writes next to it.
linux/arm64 on an amd64 host is cross-compiled with aarch64-linux-gnu-gcc.
darwin/amd64 is built with a Go toolchain rebuilt from source with scripts/go-tls-slot.patch, so the plugin's Go runtime does not share CLIProxyAPI's thread-local slot. The toolchain lands in dist/_go-tls, takes a few minutes once, and is reused while it is there. The build fails if the library still uses the stock slot.

Options:
  --version <version>       Plugin version without the leading v, as in 0.1.0. Required.
  --platform <goos/goarch>  Target platform, as in darwin/arm64. Required.
  --out <file>              Where to write the library, as in dist/darwin_arm64/expiring-quota-first.dylib. Required.
  -h, --help                Show this help.
EOF
}

# Prints the C compiler for a cross build, or nothing to leave go its default.
crossCompiler()
{
  local -r goos="${1:?goos is required}"
  local -r goarch="${2:?goarch is required}"

  if [[ "$goos/$goarch" == "linux/arm64" && "$(uname -m)" != "aarch64" ]]; then
    printf 'aarch64-linux-gnu-gcc\n'
  fi
}

buildPatchedToolchain()
{
  local -r toolchainDir="${1:?toolchainDir is required}"
  local -r patchFile="${2:?patchFile is required}"

  if [[ -x "$toolchainDir/go/bin/go" ]]; then
    return 0
  fi
  local stockVersion
  stockVersion="$(go env GOVERSION)"
  readonly stockVersion
  if [[ "$stockVersion" != "go$GO_VERSION" ]]; then
    printf 'go on PATH is %s, but the patched toolchain is pinned to go%s\n' "$stockVersion" "$GO_VERSION" >&2
    return 1
  fi

  rm -rf -- "$toolchainDir"
  mkdir -p -- "$toolchainDir"
  curl -fsSL -o "$toolchainDir/src.tar.gz" "https://go.dev/dl/go$GO_VERSION.src.tar.gz"
  printf '%s  %s\n' "$GO_SOURCE_SHA256" "$toolchainDir/src.tar.gz" | shasum -a 256 -c -
  tar -xzf "$toolchainDir/src.tar.gz" -C "$toolchainDir"
  patch -d "$toolchainDir/go" -p1 <"$patchFile"
  (cd -- "$toolchainDir/go/src" && GOROOT_BOOTSTRAP="$(go env GOROOT)" ./make.bash)
}

# Fails when any goroutine access in the library still reads the stock slot, %gs:0x30, instead of the patched one, %gs:0x58.
checkSlot()
{
  local -r library="${1:?library is required}"

  local stock patched
  stock="$(otool -tv "$library" | grep -c '%gs:0x30' || true)"
  patched="$(otool -tv "$library" | grep -c '%gs:0x58' || true)"
  readonly stock patched
  printf 'Goroutine slot accesses: patched slot x%s, stock slot x%s\n' "$patched" "$stock"
  if ((stock > 0 || patched == 0)); then
    printf '%s still uses the stock thread-local slot\n' "$library" >&2
    return 1
  fi
}

main()
{
  local version="" platform="" out=""
  while (($# > 0)); do
    case "$1" in
      -h | --help)
        printHelp
        return 0
        ;;
      --version)
        version="${2:?--version needs a value}"
        shift 2
        ;;
      --platform)
        platform="${2:?--platform needs a value}"
        shift 2
        ;;
      --out)
        out="${2:?--out needs a value}"
        shift 2
        ;;
      *)
        printHelp >&2
        return 2
        ;;
    esac
  done
  readonly version platform
  if [[ -z "$version" || -z "$platform" || -z "$out" || "$platform" != */* ]]; then
    printHelp >&2
    return 2
  fi
  local -r goos="${platform%/*}"
  local -r goarch="${platform#*/}"

  local repoDir
  repoDir="$(cd -P -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
  readonly repoDir
  mkdir -p -- "$(dirname -- "$out")"
  out="$(cd -P -- "$(dirname -- "$out")" && pwd -P)/$(basename -- "$out")"
  readonly out

  local goBinary="go"
  local -a buildEnv=(CGO_ENABLED=1 "GOOS=$goos" "GOARCH=$goarch" GOTOOLCHAIN=local GOFLAGS=-mod=readonly)
  local compiler
  compiler="$(crossCompiler "$goos" "$goarch")"
  readonly compiler
  if [[ -n "$compiler" ]]; then
    buildEnv+=("CC=$compiler")
  fi
  if [[ "$goos" == "darwin" ]]; then
    buildEnv+=("CGO_CFLAGS=-O2 -g -mmacosx-version-min=$MACOS_MIN_VERSION" "CGO_LDFLAGS=-mmacosx-version-min=$MACOS_MIN_VERSION")
  fi
  if [[ "$platform" == "darwin/amd64" ]]; then
    buildPatchedToolchain "$repoDir/dist/_go-tls" "$repoDir/scripts/go-tls-slot.patch"
    goBinary="$repoDir/dist/_go-tls/go/bin/go"
  fi
  readonly goBinary

  (cd -- "$repoDir" && env "${buildEnv[@]}" "$goBinary" build -trimpath -buildvcs=false -buildmode=c-shared -ldflags "-X main.version=$version" -o "$out" .)
  rm -f -- "${out%.*}.h"
  if [[ "$platform" == "darwin/amd64" ]]; then
    checkSlot "$out"
  fi
  printf 'Built %s\n' "$out"
}

main "$@"
