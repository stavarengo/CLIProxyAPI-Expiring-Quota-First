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
readonly DEFAULT_PLATFORMS="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64"
readonly UPSTREAM_REPO="https://github.com/router-for-me/CLIProxyAPI.git"

printHelp()
{
  cat <<EOF
Usage: ${0##*/} --dir <dir> --version <version> [--platforms "<goos/goarch> ..."]
       ${0##*/} --tag <tag> [--platforms "<goos/goarch> ..."]

Checks a release with the plugin store installer code of the CLIProxyAPI version in go.mod: clones that version and runs testdata/release_verify_test.go inside its internal/pluginstore package.
Also checks that registry.json parses and names this plugin as a GitHub release install. Needs go and git.

Modes:
  --dir <dir> --version <version>  Check built assets in <dir>: the zips and checksums.txt of plugin version <version>, without the leading v.
  --tag <tag>                      Install the published GitHub release <tag> of this repository the way CLIProxyAPI does, and check it is the latest release. GH_TOKEN is used when set; a private fork needs it.

Options:
  --platforms "<goos/goarch> ..."  Platforms to check. Default: $DEFAULT_PLATFORMS.
  -h, --help                       Show this help.
EOF
}

readCliproxyapiVersion()
{
  local -r goMod="${1:?goMod is required}"

  local line
  while IFS= read -r line; do
    line="${line#"${line%%[![:space:]]*}"}"
    if [[ "$line" == "github.com/router-for-me/CLIProxyAPI/v8 "* ]]; then
      printf '%s\n' "${line##* }"
      return 0
    fi
  done <"$goMod"
  printf 'CLIProxyAPI version not found in %s\n' "$goMod" >&2
  return 1
}

cloneUpstream()
{
  local -r version="${1:?version is required}"
  local -r target="${2:?target is required}"

  git -c advice.detachedHead=false clone --quiet --depth 1 --branch "$version" "$UPSTREAM_REPO" "$target"
}

main()
{
  local dir="" version="" tag="" platforms="$DEFAULT_PLATFORMS"
  while (($# > 0)); do
    case "$1" in
      -h | --help)
        printHelp
        return 0
        ;;
      --dir)
        dir="${2:?--dir needs a value}"
        shift 2
        ;;
      --version)
        version="${2:?--version needs a value}"
        shift 2
        ;;
      --tag)
        tag="${2:?--tag needs a value}"
        shift 2
        ;;
      --platforms)
        platforms="${2:?--platforms needs a value}"
        shift 2
        ;;
      *)
        printHelp >&2
        return 2
        ;;
    esac
  done
  readonly dir version tag platforms
  if [[ -n "$tag" && (-n "$dir" || -n "$version") ]] || [[ -z "$tag" && (-z "$dir" || -z "$version") ]]; then
    printHelp >&2
    return 2
  fi

  local repoDir cliproxyapiVersion workDir
  repoDir="$(cd -P -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
  cliproxyapiVersion="$(readCliproxyapiVersion "$repoDir/go.mod")"
  workDir="$(mktemp -d)"
  readonly repoDir cliproxyapiVersion workDir
  # shellcheck disable=SC2064 # expanded now on purpose: workDir is local to main and gone when the trap fires
  trap "rm -rf -- ${workDir@Q}" EXIT

  printf 'Checking with the plugin store code of CLIProxyAPI %s\n' "$cliproxyapiVersion"
  cloneUpstream "$cliproxyapiVersion" "$workDir/CLIProxyAPI"
  cp -- "$repoDir/testdata/release_verify_test.go" "$workDir/CLIProxyAPI/internal/pluginstore/"

  local absoluteDir=""
  if [[ -n "$dir" ]]; then
    absoluteDir="$(cd -P -- "$dir" && pwd -P)"
  fi
  readonly absoluteDir

  (
    cd -- "$workDir/CLIProxyAPI"
    env \
      "PLUGIN_ID=$PLUGIN_ID" \
      "PLUGIN_PLATFORMS=$platforms" \
      "PLUGIN_REGISTRY=$repoDir/registry.json" \
      "PLUGIN_RELEASE_DIR=$absoluteDir" \
      "PLUGIN_VERSION=$version" \
      "PLUGIN_RELEASE_TAG=$tag" \
      go test -count=1 -run '^TestExpiringQuotaFirstRelease$' -v ./internal/pluginstore/
  )
}

main "$@"
