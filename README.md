# CLIProxyAPI Expiring Quota First

[![Release](https://github.com/stavarengo/CLIProxyAPI-Expiring-Quota-First/actions/workflows/ci.yml/badge.svg)](https://github.com/stavarengo/CLIProxyAPI-Expiring-Quota-First/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/stavarengo/CLIProxyAPI-Expiring-Quota-First)](https://github.com/stavarengo/CLIProxyAPI-Expiring-Quota-First/releases/latest)
[![Built for CLIProxyAPI](https://img.shields.io/badge/dynamic/regex?url=https%3A%2F%2Fraw.githubusercontent.com%2Fstavarengo%2FCLIProxyAPI-Expiring-Quota-First%2Fmain%2Fgo.mod&search=CLIProxyAPI%2Fv8%20(v%5B0-9.%5D%2B)&replace=%241&label=built%20for%20CLIProxyAPI)](https://github.com/router-for-me/CLIProxyAPI)
[![License: MIT](https://img.shields.io/github/license/stavarengo/CLIProxyAPI-Expiring-Quota-First)](LICENSE)

A scheduler plugin for [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) that spends your Claude and Codex weekly quotas before they reset.
It is for setups where two or more Claude or Codex subscriptions sit behind one CLIProxyAPI and one account keeps hitting its weekly limit while another sits idle.
A session below is one conversation of a client such as Claude Code or the Codex CLI.
An account is one of the credentials CLIProxyAPI holds for Claude or Codex.

## Features

- **Expiring quota first:** Each new session goes to the account whose weekly quota resets soonest, so no quota expires unused while another account is being drained.
- **Sticky sessions:** A session stays on its account, across CLIProxyAPI restarts too, and moves only when that account is used up, its login is rejected, or it stays missing.
- **Exhausted accounts avoided:** An account that is used up, nearly used up, or whose login was rejected with a 401 or 403 gets no new sessions while another account is ready.
- **Per-model limits:** Claude's separate weekly limit on its top model counts too. As of 2026-10 that model is Fable, on Max plans.
- **Subagents follow their parent:** A Claude Code subagent gets the same account as the session that started it.
- **Shadow mode:** The plugin starts by only recording what it would have picked, so you can check its choices before letting it route.
- **Readable state file:** Picks are logged to a [state file](#state-file) with their reasons, and session ids are stored as hashes.
- **No secrets stored, no calls out:** Quota is learned from the rate-limit headers CLIProxyAPI already receives, and the plugin makes no network calls of its own. CLIProxyAPI does hand the plugin each request's headers and the client's proxy key. The plugin reads the session id from the headers and writes neither the headers nor the key to disk.
- **Scoped to Claude and Codex:** Requests for other providers, and routes that mix providers, stay with CLIProxyAPI's own routing.

## How it picks an account

The plugin binds each session to one account and keeps the binding until the session has been idle for `ttl`.
`ttl`, `rebind_after` and `min_remaining` are settings; [Configure](#configure) has their defaults.

```mermaid
flowchart TD
    R([Claude or Codex request]) --> B{Session<br>already bound?}
    B -- yes --> O{Account still offered<br>and usable?}
    O -- yes --> K[Keep the account]
    O -- "unusable, or missing<br>longer than rebind_after" --> N
    B -- no --> N[Pick the best account]
    N --> RANK
    subgraph RANK[Best first]
        direction LR
        G1[1 Ready<br>room left, or<br>nothing known yet] ~~~ G2[2 Awaiting<br>reset passed or login rejected,<br>no response since] ~~~ G3[3 Low<br>under min_remaining %] ~~~ G4[4 Unusable<br>used up, or login<br>rejected recently]
    end
    RANK --> P[Bind the session to it]
    RANK -. every account unusable .-> C[Left to CLIProxyAPI's<br>own routing]

    classDef ready fill:#2e7d32,color:#fff,stroke:#1b5e20
    classDef waiting fill:#f9a825,color:#000,stroke:#f57f17
    classDef low fill:#ef6c00,color:#fff,stroke:#e65100
    classDef bad fill:#c62828,color:#fff,stroke:#b71c1c
    classDef neutral fill:#455a64,color:#fff,stroke:#263238
    class G1,K,P ready
    class G2 waiting
    class G3 low
    class G4 bad
    class C,R,N neutral
    style RANK fill:none,stroke:#78909c,stroke-dasharray:4,color:#b0bec5
```

Only the weekly quota counts; the five-hour limits are ignored.
Among ready accounts, the order is: first an account that has not answered a request yet, so its reset time is unknown and traffic will learn it; then the soonest weekly reset; last, accounts that have answered but report no weekly quota, such as plain API keys.
A rejected login keeps an account unusable for 30 minutes; after that it is the last resort until a request on it succeeds.
While a bound account has been missing for less than `rebind_after`, each request fails over to the best account for that request only, and the binding stays.
Claude Code subagents carry their parent's session id, so they take the parent's account.

## Known limits

- A request with no session id is left to CLIProxyAPI. The plugin looks for the session identity CLIProxyAPI attaches to the request, then for the session id headers CLIProxyAPI itself accepts.
- An account's quota is unknown until it has served a request, and a per-model weekly limit only after a request to that model has shown it.
- The plugin reads Codex's weekly window from the `X-Codex-Primary-*` and `X-Codex-Secondary-*` headers as the account's one main limit. Whether those headers describe another limit when `X-Codex-Active-Limit` names a per-model one is unconfirmed. If the headers do, the account's record blends two limits. Codex's own per-model weekly limits, such as the one on its Spark model, come under their own header prefix, which the plugin does not read, so an account out of Spark keeps getting Spark sessions.
- Of Claude's per-model weekly limits the plugin reads only Fable's, from the response headers named `7d_oi`. Claude Code's code names limits for other models too, but no capture shows which responses would carry them, so the plugin ignores them until a capture does.
- The plugin ranks only the accounts CLIProxyAPI offers. CLIProxyAPI lets you set a priority on each credential, and offers a plugin only the accounts of the highest priority that still has a usable one, unless the plugin declares the `scheduler_across_priorities` capability, which this one does not. So a lower-priority account with an earlier reset waits until every higher-priority account is unusable. Without priorities, every account is offered.
- Under CLIProxyAPI's Home mode, the control-plane mode its `-home-jwt` flag turns on, every pick goes through Home and no scheduler plugin is asked. The plugin loads but never decides.
- Releases are Linux only; [Install](#install) says which platforms, and [Develop](#develop) how to build for another.

## Quick start

1. Add this repository's `registry.json` as a plugin store source in CLIProxyAPI's `config.yaml`. Skip this step when the plugin store page of your management console already lists the plugin:

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

3. Turn plugins on and start in `shadow` mode. The store install has already written the plugin's own `enabled`. The global `plugins.enabled` it has not, and CLIProxyAPI treats that one as off while it is missing, so set it:

   ```yaml
   plugins:
     enabled: true
     configs:
       expiring-quota-first:
         enabled: true
         mode: shadow
   ```

4. After some traffic, read the picks in the [state file](#state-file). Once they are the accounts you would have chosen, set `mode: active`.

## Install

Each release is built for the CLIProxyAPI version in `go.mod` at the time, shown in the badge above and in the release notes.
When the plugin interface of your CLIProxyAPI differs, CLIProxyAPI logs `pluginhost: failed to load plugin` and routes with its own rules.

Releases cover linux/amd64 and linux/arm64.
They are built on the same Debian version as the official `eceasy/cli-proxy-api` image, pinned in `build.sh`, so they need no newer glibc than that image has.
The arm64 library is cross-compiled on amd64 and checked for its machine type and entry symbol only.
No arm64 host has loaded it in the release pipeline, so whether it loads there is unconfirmed, and an issue reporting either outcome is welcome.
For another platform, [build it yourself](#develop).

The store install from the [quick start](#quick-start) checks the download against the release's `checksums.txt`, installs it into CLIProxyAPI's `plugins` folder and loads it without a restart.
To install by hand:

1. Download `expiring-quota-first_<version>_<os>_<arch>.zip` from the [latest release](https://github.com/stavarengo/CLIProxyAPI-Expiring-Quota-First/releases/latest) and check it against `checksums.txt`.
2. Put the `expiring-quota-first.so` inside the zip into the `plugins` folder. Keep the file name: CLIProxyAPI takes the plugin id from it.
3. Add the plugin's config block as in the [quick start](#quick-start), including the plugin's own `enabled: true`, which only the store install writes for you.
4. Restart CLIProxyAPI.

## Configure

The full set in CLIProxyAPI's `config.yaml`. Apart from the two `enabled` flags, every value shown is the default:

```yaml
plugins:
  enabled: true  # CLIProxyAPI's own flag; off while missing
  dir: "plugins"
  configs:
    expiring-quota-first:
      enabled: true
      priority: 0       # order among scheduler plugins, CLIProxyAPI's own; not the credential priority
      mode: shadow      # or active
      ttl: 168h
      rebind_after: 15m
      min_remaining: 5  # percent of the weekly quota
      state_file: "plugins/expiring-quota-first-data/state.json"  # relative to CLIProxyAPI's working directory, /CLIProxyAPI in the official image
```

In the official image the default `state_file` lands in `/CLIProxyAPI/plugins`.
The Docker Compose file that ships with CLIProxyAPI mounts that folder from the host, so the state survives a recreated container.
If you start the container another way, or set another `dir`, choose a `state_file` on a mounted path. Otherwise bindings and learned quota start over with each new container.

The plugin picks up config changes live and keeps what it learned about each account's quota.
Switching `mode` drops the session bindings, since a shadow binding names the account the plugin would have picked, not the one CLIProxyAPI used.

## State file

JSON, written shortly after something changes and on shutdown, with times in RFC 3339 UTC:

- `bindings`: one entry per session, keyed by a short hash of the session id so no raw id lands on disk. It names the `auth_id` the session is on, when the session was `last_seen`, and `missing_since` when that account stopped being offered.
- `accounts`: what the response headers have said about each account, keyed by the same credential id as `auth_id`, the one the management API lists. `weekly_reset` and `weekly_used`, a fraction from 0 to 1, describe the current weekly quota. `full_until` is set while the weekly quota is known to be used up, and `model_full_until` says the same per model family, such as `fable`. `auth_failed` is the status, 401 or 403, of the last rejected login with no success since, received at `auth_failed_at`. `served` is true once the account has answered a request.
- `models`: each route alias seen in usage, such as a `best` that maps to Fable, to the upstream model it ran as. So a per-model limit also covers picks that name the alias.
- `recent_decisions`: the last 200 picks, oldest first. Each has the time, the hashed session, the model, the `auth_id`, a short `reason` such as `new session; soonest weekly reset`, and `applied`. `applied` is true only when the plugin routed the request, so never in shadow mode.
- `mode`: the mode the bindings were made in, since a mode switch drops them.

A file that cannot be parsed is renamed to `state.json.corrupt` and the plugin starts fresh.

## Develop

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

## License

[MIT](LICENSE)
