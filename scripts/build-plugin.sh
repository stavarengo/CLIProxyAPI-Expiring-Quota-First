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
Vets and tests the plugin, builds it for ${PLATFORMS[*]} with scripts/build-library.sh, then zips, checksums and verifies the result with scripts/package-release.sh.

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
  rm -rf -- "$distDir"

  local platform
  for platform in "${PLATFORMS[@]}"; do
    scripts/build-library.sh --version "$version" --platform "$platform" --out "$distDir/${platform/\//_}/$PLUGIN_ID.so"
  done
  scripts/package-release.sh --version "$version" --libraries "$distDir" --out "$distDir/release" --platforms "${PLATFORMS[*]}" --epoch "$sourceDateEpoch"

  # Leave the output owned by whoever owns the checkout, not by the container's root.
  chown -R --reference=. -- "$distDir"
}

main "$@"
