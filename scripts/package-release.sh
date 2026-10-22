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

printHelp()
{
  cat <<EOF
Usage: ${0##*/} --version <version> --libraries <dir> --out <dir> [--platforms "<goos/goarch> ..."] [--epoch <unix time>]

Zips the library built for each platform, <dir>/<goos>_<goarch>/$PLUGIN_ID.<so|dylib|dll>, the way CLIProxyAPI's plugin store expects: one ${PLUGIN_ID}_<version>_<goos>_<goarch>.zip per platform with the library at its root, and checksums.txt in sha256sum format.
Then checks the result with scripts/verify-release.sh. Every platform listed must have its library. Needs zip, sha256sum, GNU touch, go and git.

Options:
  --version <version>              Plugin version without the leading v, as in 0.1.0. Required.
  --libraries <dir>                Folder holding one <goos>_<goarch> folder per platform. Required.
  --out <dir>                      Where the zips and checksums.txt go; earlier ones there are replaced. Required.
  --platforms "<goos/goarch> ..."  Platforms to package. Default: $DEFAULT_PLATFORMS.
  --epoch <unix time>              Time stamped on the zip entries, so the same libraries give the same zips. Default: the time of the checkout's last commit.
  -h, --help                       Show this help.
EOF
}

libraryExtension()
{
  local -r goos="${1:?goos is required}"

  case "$goos" in
    darwin) printf '.dylib\n' ;;
    windows) printf '.dll\n' ;;
    *) printf '.so\n' ;;
  esac
}

zipPlatform()
{
  local -r library="${1:?library is required}"
  local -r zipFile="${2:?zipFile is required}"
  local -r epoch="${3:?epoch is required}"

  touch -d "@$epoch" -- "$library"
  rm -f -- "$zipFile"
  # -j keeps the library at the zip root, where the installer wants it; -X drops the extra fields that vary between runs.
  zip -q -j -X "$zipFile" "$library"
}

main()
{
  local version="" libraries="" out="" platforms="$DEFAULT_PLATFORMS" epoch=""
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
      --libraries)
        libraries="${2:?--libraries needs a value}"
        shift 2
        ;;
      --out)
        out="${2:?--out needs a value}"
        shift 2
        ;;
      --platforms)
        platforms="${2:?--platforms needs a value}"
        shift 2
        ;;
      --epoch)
        epoch="${2:?--epoch needs a value}"
        shift 2
        ;;
      *)
        printHelp >&2
        return 2
        ;;
    esac
  done
  readonly version libraries out platforms
  if [[ -z "$version" || -z "$libraries" || -z "$out" ]]; then
    printHelp >&2
    return 2
  fi

  local repoDir
  repoDir="$(cd -P -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
  readonly repoDir
  if [[ -z "$epoch" ]]; then
    epoch="$(git -C "$repoDir" log -1 --format=%ct)"
  fi
  readonly epoch

  mkdir -p -- "$out"
  rm -f -- "$out"/*.zip "$out/checksums.txt"

  local platform goos goarch library
  for platform in $platforms; do
    goos="${platform%/*}"
    goarch="${platform#*/}"
    library="$libraries/${goos}_${goarch}/$PLUGIN_ID$(libraryExtension "$goos")"
    if [[ ! -f "$library" ]]; then
      printf 'No library for %s at %s\n' "$platform" "$library" >&2
      return 1
    fi
    zipPlatform "$library" "$out/${PLUGIN_ID}_${version}_${goos}_${goarch}.zip" "$epoch"
  done
  (cd -- "$out" && sha256sum -- *.zip >|checksums.txt)

  "$repoDir/scripts/verify-release.sh" --dir "$out" --version "$version" --platforms "$platforms"
  printf 'Release assets in %s\n' "$out"
}

main "$@"
