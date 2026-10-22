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

# Bookworm, like the eceasy/cli-proxy-api image, so the plugin needs no newer glibc than the proxy has.
# Pinned by digest: the plugin runs inside the process that holds the users' OAuth tokens, so a rebuild of the same tag must get the same toolchain.
readonly BUILD_IMAGE="golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5"

printHelp()
{
  cat <<EOF
Usage: ${0##*/} [--version <version>]

Builds the plugin for linux/amd64 and linux/arm64 inside ${BUILD_IMAGE%%@*}, after vet and the tests, then checks the result with the plugin store installer code of the CLIProxyAPI version in go.mod.
Writes the assets a release needs to dist/release: one zip per platform and checksums.txt.

Options:
  --version <version>  Plugin version without the leading v, as in 0.1.0. Default: 0.0.0-dev.
  -h, --help           Show this help.
EOF
}

main()
{
  local version="0.0.0-dev"
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
      *)
        printHelp >&2
        return 2
        ;;
    esac
  done
  readonly version

  local repoDir sourceDateEpoch
  repoDir="$(cd -P -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
  # The zip entries take the commit's time, so the same commit gives the same archive.
  sourceDateEpoch="$(git -C "$repoDir" log -1 --format=%ct)"
  readonly repoDir sourceDateEpoch

  docker run --rm \
    --platform linux/amd64 \
    --volume "$repoDir:/src" \
    --workdir /src \
    --env "PLUGIN_VERSION=$version" \
    --env "SOURCE_DATE_EPOCH=$sourceDateEpoch" \
    "$BUILD_IMAGE" \
    scripts/build-plugin.sh
}

main "$@"
