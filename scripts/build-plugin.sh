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

readonly PLUGIN_ID="expiring-quota-first"
readonly PLATFORMS=(linux/amd64 linux/arm64)

printHelp()
{
  cat <<EOF
Usage: ${0##*/}

Runs inside the Go image that build.sh starts; build.sh is the entry point.
Vets and tests the plugin, builds it for ${PLATFORMS[*]}, zips each build the way CLIProxyAPI's plugin store expects, writes checksums.txt, and verifies the result with scripts/verify-release.sh.

Environment:
  PLUGIN_VERSION     Plugin version without the leading v. Required.
  SOURCE_DATE_EPOCH  Unix time stamped on the zip entries. Required.

Options:
  -h, --help  Show this help.
EOF
}

installBuildDeps()
{
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends zip gcc-aarch64-linux-gnu libc6-dev-arm64-cross
}

buildPlatform()
{
  local -r goos="${1:?goos is required}"
  local -r goarch="${2:?goarch is required}"
  local -r outFile="${3:?outFile is required}"
  local -r version="${4:?version is required}"

  local cc="gcc"
  if [[ "$goarch" == "arm64" ]]; then
    cc="aarch64-linux-gnu-gcc"
  fi
  readonly cc

  mkdir -p -- "$(dirname -- "$outFile")"
  CGO_ENABLED=1 GOOS="$goos" GOARCH="$goarch" CC="$cc" go build -trimpath -buildvcs=false -buildmode=c-shared -ldflags "-X main.version=$version" -o "$outFile" .
}

zipPlatform()
{
  local -r library="${1:?library is required}"
  local -r zipFile="${2:?zipFile is required}"
  local -r sourceDateEpoch="${3:?sourceDateEpoch is required}"

  touch -d "@$sourceDateEpoch" -- "$library"
  mkdir -p -- "$(dirname -- "$zipFile")"
  rm -f -- "$zipFile"
  # -j keeps the library at the zip root, where the installer wants it; -X drops the extra fields that vary between runs.
  zip -q -j -X "$zipFile" "$library"
}

main()
{
  case "${1:-}" in
    -h | --help)
      printHelp
      return 0
      ;;
    "") ;;
    *)
      printHelp >&2
      return 2
      ;;
  esac
  local -r version="${PLUGIN_VERSION:?PLUGIN_VERSION is required}"
  local -r sourceDateEpoch="${SOURCE_DATE_EPOCH:?SOURCE_DATE_EPOCH is required}"

  export GOTOOLCHAIN=local GOFLAGS=-mod=readonly

  installBuildDeps
  go vet ./...
  go test ./...

  local -r distDir="$PWD/dist"
  local -r releaseDir="$distDir/release"
  rm -rf -- "$distDir"

  local platform goos goarch
  for platform in "${PLATFORMS[@]}"; do
    goos="${platform%/*}"
    goarch="${platform#*/}"
    buildPlatform "$goos" "$goarch" "$distDir/${goos}_${goarch}/$PLUGIN_ID.so" "$version"
    zipPlatform "$distDir/${goos}_${goarch}/$PLUGIN_ID.so" "$releaseDir/${PLUGIN_ID}_${version}_${goos}_${goarch}.zip" "$sourceDateEpoch"
  done
  (cd -- "$releaseDir" && sha256sum -- *.zip >|checksums.txt)

  scripts/verify-release.sh --dir "$releaseDir" --version "$version"

  # Leave the output owned by whoever owns the checkout, not by the container's root.
  chown -R --reference=. -- "$distDir"
  printf 'Release assets in %s\n' "$releaseDir"
}

main "$@"
