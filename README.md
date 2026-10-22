# CLIProxyAPI Expiring Quota First

[![CI](https://github.com/stavarengo/CLIProxyAPI-Expiring-Quota-First/actions/workflows/ci.yml/badge.svg)](https://github.com/stavarengo/CLIProxyAPI-Expiring-Quota-First/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/stavarengo/CLIProxyAPI-Expiring-Quota-First)](https://github.com/stavarengo/CLIProxyAPI-Expiring-Quota-First/releases/latest)
[![Built for CLIProxyAPI](https://img.shields.io/badge/dynamic/regex?url=https%3A%2F%2Fraw.githubusercontent.com%2Fstavarengo%2FCLIProxyAPI-Expiring-Quota-First%2Fmain%2Fgo.mod&search=CLIProxyAPI%2Fv8%20(v%5B0-9.%5D%2B)&replace=%241&label=built%20for%20CLIProxyAPI)](https://github.com/router-for-me/CLIProxyAPI)
[![License: MIT](https://img.shields.io/github/license/stavarengo/CLIProxyAPI-Expiring-Quota-First)](LICENSE)

A scheduler plugin for [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) that spends your Claude and Codex weekly quotas before they reset.
Each new session goes to the account whose weekly quota resets soonest and stays there, across CLIProxyAPI restarts too.
It is for setups where two or more Claude or Codex subscriptions sit behind one CLIProxyAPI and one account keeps hitting its weekly limit while another sits idle.

It reads the quota from the rate-limit headers CLIProxyAPI already receives, so it holds no tokens and makes no network calls of its own.
Other providers, and setups with a single account, are left to CLIProxyAPI's own routing.

## Quick start

1. Add this repository's `registry.json` as a plugin store source in CLIProxyAPI's `config.yaml` (not needed once the plugin is in the official store):

   ```yaml
   plugins:
     store-sources:
       - "https://raw.githubusercontent.com/stavarengo/CLIProxyAPI-Expiring-Quota-First/main/registry.json"
   ```

2. Install `Expiring Quota First` from the plugin store page of your management console, or through the management API:

   ```bash
   curl -X POST -H "Authorization: Bearer <management key>" \
     http://<cliproxyapi>/v8/management/plugins/store/expiring-quota-first/install
   ```

3. Turn plugins on globally and start in `shadow` mode, where the plugin only records what it would pick. The store install already set the plugin's own `enabled`:

   ```yaml
   plugins:
     enabled: true
     configs:
       expiring-quota-first:
         enabled: true
         mode: shadow
   ```

4. After some traffic, read the picks in the [state file](#state-file). Once they are the accounts you would have chosen, set `mode: active`.

## How it picks an account

Only the weekly quota counts; the five-hour limits are ignored.
For a new session, the accounts CLIProxyAPI offers are ranked best first:

1. Ready: room left, or nothing known yet. Accounts with an unknown reset time go first so traffic learns it, then the soonest weekly reset, then accounts whose responses carry no weekly quota at all, such as plain API keys.
2. Awaiting: used up but past its reset time, with no response yet confirming the new quota. Or its login was rejected and nothing has succeeded since: at once for a 401 on an account CLIProxyAPI keeps offering, after half an hour for the rejections of rank 4.
3. Low: under `min_remaining` percent of its weekly quota.
4. Unusable: weekly quota used up, for every model or for the requested one, or within the last half hour a 403, or a 401 on an account CLIProxyAPI has since stopped offering. When every account is unusable, the request is left to CLIProxyAPI.

The session keeps its account until it has been idle for `ttl`.
If CLIProxyAPI stops offering that account, each request falls back on its own and the session rebinds after `rebind_after`; if the account becomes unusable, it rebinds at once.
Claude Code subagents carry their parent's session id, so they take the parent's account.

## Install

Each release is tested with one CLIProxyAPI version.
When the plugin interface differs, CLIProxyAPI logs `pluginhost: failed to load plugin` and routes with its own rules.

| Plugin release | CLIProxyAPI |
| -------------- | ----------- |
| v0.1.1         | v8.0.33     |
| v0.1.0         | v8.0.33     |

Releases cover linux/amd64 and linux/arm64, built on Debian bookworm like the official `eceasy/cli-proxy-api` image; elsewhere they need glibc 2.36 or newer.
For another platform, [build it yourself](#develop).

The store install from the [quick start](#quick-start) checks the download against the release's `checksums.txt`, installs it into CLIProxyAPI's `plugins` folder and loads it without a restart.
To install by hand, download `expiring-quota-first_<version>_<os>_<arch>.zip` from the [latest release](https://github.com/stavarengo/CLIProxyAPI-Expiring-Quota-First/releases/latest), check it against `checksums.txt`, put the `expiring-quota-first.so` inside into the `plugins` folder without renaming it (CLIProxyAPI takes the plugin id from the file name), and restart CLIProxyAPI.

## Configure

The full set with its defaults, in CLIProxyAPI's `config.yaml`:

```yaml
plugins:
  enabled: true      # CLIProxyAPI's own flag; off while missing
  dir: "plugins"
  configs:
    expiring-quota-first:
      enabled: true      # set by a store install; set it yourself after a hand install
      priority: 0        # CLIProxyAPI's own; only matters with several scheduler plugins
      mode: shadow       # shadow records what it would pick, active routes
      ttl: 168h          # idle time before a session forgets its account
      rebind_after: 15m  # how long a bound account may be missing before the session moves
      min_remaining: 5   # percent of weekly quota under which an account gets no new sessions while a readier one exists
      state_file: "plugins/expiring-quota-first-data/state.json"  # relative to CLIProxyAPI's working directory, /CLIProxyAPI in the official image
```

Config changes are picked up live without losing what was learned.
Switching `mode` drops the session bindings, since a shadow binding names the account the plugin would have picked, not the one CLIProxyAPI used.

## State file

JSON, written every ten seconds while something changed and on shutdown, with times in RFC 3339 UTC:

- `bindings`: each session, as a short hash so no raw session id lands on disk, to its `auth_id`, with `last_seen` and `missing_since`.
- `accounts`: per credential id, the one the management API lists: `weekly_reset`, `weekly_used` as a fraction from 0 to 1, `full_until`, `model_full_until` per model family such as `opus`, `auth_failed` (401 or 403) with `auth_failed_at`, and `served`.
- `recent_decisions`: the last 200 picks, oldest first, each with time, hashed session, model, `auth_id`, a short `reason` such as `new session; soonest weekly reset`, and `applied`, true only when the plugin routed the request, so never in shadow mode.
- `mode`: the mode the bindings were made in.

A file that cannot be parsed is renamed to `state.json.corrupt` and the plugin starts fresh.

## Known limits

- Only Claude and Codex routes are handled; other providers and mixed routes go to CLIProxyAPI's own routing.
- A request with no session id, neither the one CLIProxyAPI derives nor an `X-Claude-Code-Session-Id`, `Session-Id`, `Session_id` or `X-Session-Id` header, is left to CLIProxyAPI.
- An account's quota is unknown until it has served a request, and a per-model weekly limit, such as Claude's Opus limit, only after the first request it refuses.
- Releases are Linux only.

## Develop

The tests need the Go version in `go.mod` and a C compiler:

```bash
go vet ./... && go test -race ./...
```

`./build.sh --version <version>` reproduces the CI build and needs Docker and Bash 5.2.15 or newer: it builds, tests and zips both platforms into `dist/release`, then installs them with CLIProxyAPI's own plugin store code as a check.
For another platform, build on it with cgo enabled:

```bash
go build -trimpath -buildmode=c-shared -ldflags "-X main.version=<version>" -o expiring-quota-first.so .
```

To release, push a tag `v<version>`; CI publishes the GitHub release and runs the same install check against it.
Issues and pull requests are welcome.

## License

[MIT](LICENSE)
