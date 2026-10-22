# CLIProxyAPI Expiring Quota First

[![CI](https://github.com/stavarengo/CLIProxyAPI-Expiring-Quota-First/actions/workflows/ci.yml/badge.svg)](https://github.com/stavarengo/CLIProxyAPI-Expiring-Quota-First/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/stavarengo/CLIProxyAPI-Expiring-Quota-First)](https://github.com/stavarengo/CLIProxyAPI-Expiring-Quota-First/releases/latest)
[![Built for CLIProxyAPI](https://img.shields.io/badge/dynamic/regex?url=https%3A%2F%2Fraw.githubusercontent.com%2Fstavarengo%2FCLIProxyAPI-Expiring-Quota-First%2Fmain%2Fgo.mod&search=CLIProxyAPI%2Fv8%20(v%5B0-9.%5D%2B)&replace=%241&label=built%20for%20CLIProxyAPI)](https://github.com/router-for-me/CLIProxyAPI)
[![License: MIT](https://img.shields.io/github/license/stavarengo/CLIProxyAPI-Expiring-Quota-First)](LICENSE)

A scheduler plugin for [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) that spends your Claude and Codex weekly quotas before they reset.
Each new session, meaning one conversation of a client such as Claude Code or the Codex CLI, goes to the account with quota left whose weekly quota resets soonest, and the session then stays on that account while it has quota, across CLIProxyAPI restarts too.
Accounts that are used up or whose login was rejected get no sessions, and an account under 5 percent of its weekly quota, a threshold you can change, gets no new sessions while another one is ready.

It is for you if two or more Claude or Codex subscriptions sit behind one CLIProxyAPI and you keep hitting one account's weekly limit while quota on another one goes unused.
With a single account, or with other providers, it changes nothing: those requests stay with CLIProxyAPI's own routing.
Each release is built for one CLIProxyAPI version, named in the badge above and in [Install](#install).

It reads the quota from the rate-limit headers CLIProxyAPI already receives, so it holds no tokens and makes no network calls of its own.
Like every CLIProxyAPI plugin, it runs inside the proxy's process.

## Quick start

1. Add this repository's `registry.json`, the plugin's store listing, as an extra plugin store source in CLIProxyAPI's `config.yaml`. This step goes away once the plugin is in the official store:

   ```yaml
   plugins:
     store-sources:
       - "https://raw.githubusercontent.com/stavarengo/CLIProxyAPI-Expiring-Quota-First/main/registry.json"
   ```

2. Install `Expiring Quota First` from the plugin store page of the management console you use with CLIProxyAPI, or through CLIProxyAPI's management API:

   ```bash
   curl -X POST -H "Authorization: Bearer <management key>" \
     http://<cliproxyapi>/v8/management/plugins/store/expiring-quota-first/install
   ```

3. Enable it, in `shadow` mode, where it only records what it would pick. Both the global `enabled` and the plugin's own are needed, since CLIProxyAPI treats a missing one as off:

   ```yaml
   plugins:
     enabled: true
     configs:
       expiring-quota-first:
         enabled: true
         mode: shadow
   ```

4. Let it watch traffic for a while, read the accounts it would have picked in its [state file](#reading-the-state-file), and set `mode: active` once they are the ones you would have picked.

## How it chooses an account

CLIProxyAPI tells sessions apart by the session id the client sends, and asks the plugin which of the accounts it has for that provider should serve the request.
Claude and Codex both report a weekly quota in their response headers, with the time it resets.
The plugin reads that and ignores the shorter five-hour limits.

For a new session, every account CLIProxyAPI offers is ranked, best first:

1. Ready: it has room left, or nothing is known about it yet.
2. Awaiting: it was used up and its reset time has passed, but no response has confirmed the new weekly quota yet. Only traffic can confirm it, so when no account is ready a new session goes here rather than to an account that is low.
3. Low: under the `min_remaining` percentage of its weekly quota, 5 by default.
4. Unusable: its weekly quota is used up, for every model or for the requested one, or its login was rejected with a 401 or 403 status. When every account is unusable, the request is left to CLIProxyAPI.

Among ready accounts, one that has not served a request yet, so its reset time is unknown, goes first, and that single session learns it.
Then comes the soonest reset, and last the accounts whose responses carry no weekly quota at all, such as plain API keys.

The chosen account is recorded as the session's binding, and the session keeps it until it has been idle for `ttl`, a week by default.
If CLIProxyAPI stops offering that account, each request falls back to the best other account on its own, and the session rebinds for good after `rebind_after`, 15 minutes by default.
If the account becomes unusable, the session rebinds at once.
A subagent that Claude Code starts inside a session, which CLIProxyAPI reports with its parent's session id, takes the parent's account.

Every pick is recorded in the state file with a reason that uses the words of this section.
[Configure](#configure) describes the settings named here.

## Install

Each release is built for one CLIProxyAPI version, the one in `go.mod` at the time, and the release notes name it.
When the plugin interface of your CLIProxyAPI differs from the one the release was built for, CLIProxyAPI refuses the plugin with a `pluginhost: failed to load plugin` warning in its log, then routes with its own rules.
Another CLIProxyAPI version may still load a release when its plugin interface is unchanged, but only these pairs are tested:

| Plugin release | CLIProxyAPI |
| -------------- | ----------- |
| v0.1.1         | v8.0.33     |
| v0.1.0         | v8.0.33     |

Releases cover linux/amd64 and linux/arm64, built on Debian bookworm like CLIProxyAPI's official Docker image, `eceasy/cli-proxy-api`, so they need no newer glibc than that image has.
On another Linux system they need glibc 2.36 or newer.
For another platform, [build it yourself](#build).

### From the plugin store

Follow the [quick start](#quick-start).
The store checks the download against the release's `checksums.txt`, installs it under CLIProxyAPI's `plugins` folder and loads it without a restart.

### By hand

Download `expiring-quota-first_<version>_<os>_<arch>.zip` from the [release](https://github.com/stavarengo/CLIProxyAPI-Expiring-Quota-First/releases/latest), check it against `checksums.txt`, and put the `expiring-quota-first.so` inside into CLIProxyAPI's `plugins` folder.
Keep the file name, because CLIProxyAPI takes the plugin id from it.
Then restart CLIProxyAPI.

## Configure

Everything lives under `plugins.configs.expiring-quota-first` in CLIProxyAPI's `config.yaml`.
This is the full set, with every value at its default except the two `enabled` flags, which are off until you set them:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    expiring-quota-first:
      enabled: true
      priority: 0
      mode: shadow
      ttl: 168h
      rebind_after: 15m
      min_remaining: 5
      state_file: "plugins/expiring-quota-first-data/state.json"
```

The first four keys belong to CLIProxyAPI, which reads them for every plugin: the global `enabled`, the `dir` it loads plugins from, and the per-plugin `enabled` and `priority`.
A higher `priority` runs before other scheduler plugins; with this plugin alone, it makes no difference.

The plugin's own fields:

- `mode` is `shadow` or `active`. In `shadow` the plugin only records what it would pick and CLIProxyAPI keeps routing by its own rules. In `active` the plugin routes. Switching mode drops the session bindings: a binding made in shadow names the account the plugin would have picked, not the one CLIProxyAPI actually used.
- `ttl` is how long a session may be idle before it forgets its account. It is a Go duration, such as `168h` or `30m`.
- `rebind_after` is how long a session's account may be missing from the accounts CLIProxyAPI offers before the session moves to another account. Until then each request falls back on its own.
- `min_remaining` is a percentage of the weekly quota. An account under it gets no new sessions while a readier one exists; sessions already on it stay.
- `state_file` is where bindings, what was learned about each account and the recent picks are kept. A relative path is resolved from CLIProxyAPI's working directory. In the official Docker image that directory is `/CLIProxyAPI`, so the default lands inside the `plugins` folder you already mount.

CLIProxyAPI reloads the config when it changes, and the plugin picks up new values without losing what it learned, apart from the bindings on a mode switch.

## Reading the state file

The state file is JSON, written every ten seconds while something changed, and on shutdown.
Times are RFC 3339 in UTC.
Its four parts:

- `mode` is the mode the bindings were made in.
- `bindings` maps each session to its account. The key is a short hash of the session id, so no raw session id lands on disk. `auth_id` is the account, `last_seen` the last request, and `missing_since` is set while CLIProxyAPI stops offering that account.
- `accounts` holds what the response headers have said about each account, keyed by the id CLIProxyAPI gives the credential, the same one its management API lists:
  - `weekly_reset` and `weekly_used`, a fraction from 0 to 1, describe the current weekly quota.
  - `full_until` is set while the weekly quota is known to be used up, and `model_full_until` says the same per model family, such as `opus`.
  - `auth_failed` is the status code, 401 or 403, of the last rejected login with no success since, received at `auth_failed_at`.
  - `served` is true once the account has answered a request. An account that has served but has no `weekly_reset` is one whose responses carry no weekly quota, and it ranks last for new sessions.
- `recent_decisions` is the last 200 picks, oldest first. Each has the time, the hashed session, the model, the chosen `auth_id`, the `reason` in the words of [How it chooses an account](#how-it-chooses-an-account), and `applied`, which is true only when the plugin routed the request, so always false in shadow mode.

A file that cannot be parsed is renamed to `state.json.corrupt` and the plugin starts fresh, so a bad file can still be looked at.

## Known limits

- Only Claude and Codex routes are handled. Requests for other providers, and routes that mix providers, go to CLIProxyAPI's own routing.
- A request with no session id, neither the one CLIProxyAPI derives nor a `Session-Id` or `X-Claude-Code-Session-Id` header, is left to CLIProxyAPI.
- Quota is learned from responses, so an account is unknown until it has served a request. A per-model weekly limit, such as Claude's Opus limit, is only known after the first request it refuses.
- The releases are Linux only.

## Build

`./build.sh --version <version>` builds, tests and zips the plugin for both platforms in a Go Docker image pinned by digest in that script, and writes the release assets to `dist/release`.
It ends by installing them with the code CLIProxyAPI's plugin store uses, at the CLIProxyAPI version in `go.mod`, the same check CI runs on every push.

For another platform, build on that platform with cgo enabled, which needs a C compiler:

```bash
go build -trimpath -buildmode=c-shared -ldflags "-X main.version=<version>" -o expiring-quota-first.so .
```

## Release

Push a tag `v<version>`, for example `v0.1.0`.
CI builds the assets, publishes the GitHub release, and then installs that release through CLIProxyAPI's plugin store code as a final check.

## Contributing

Issues and pull requests are welcome.
The tests need the Go version in `go.mod` and a C compiler:

```bash
go vet ./... && go test -race ./...
```

`./build.sh` reproduces the CI build locally and needs Docker.
CI runs on every push and pull request.

## License

[MIT](LICENSE)
