# CLIProxyAPI Expiring Quota First

A scheduler plugin for [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) that sends each new session to the account whose weekly quota resets soonest, and keeps a session on its account, across restarts too.
It skips accounts that are used up or logged out, and avoids accounts with little weekly quota left while another one is ready.

It learns quota from the response headers CLIProxyAPI already receives, so it holds no tokens and makes no network calls.
A per-model limit is only known after the first request that limit refuses.

## Install

Each release is built for one CLIProxyAPI version, the one pinned in `go.mod` at the time, and the release notes name it.
CLIProxyAPI drops a plugin built for another ABI and keeps its own routing without logging why, so match the versions:

| Plugin release | CLIProxyAPI |
| -------------- | ----------- |
| v0.1.0         | v8.0.33     |

Releases cover linux/amd64 and linux/arm64, built on Debian bookworm like the `eceasy/cli-proxy-api` image.

### From the plugin store

Until the plugin is listed in the official store, add this repository's `registry.json` as an extra store source, then install `expiring-quota-first` from the plugin store in CLIProxyAPI's management console:

```yaml
plugins:
  store-sources:
    - "https://raw.githubusercontent.com/stavarengo/CLIProxyAPI-Expiring-Quota-First/main/registry.json"
```

### By hand

Download `expiring-quota-first_<version>_<os>_<arch>.zip` from the release, check it against `checksums.txt`, and put the `expiring-quota-first.so` inside into CLIProxyAPI's `plugins` folder.
Keep the file name, because the plugin id is the file's basename.
Then restart CLIProxyAPI.

## Build

`./build.sh --version <version>` builds, tests and zips the plugin for both platforms in a pinned Docker image and writes the release assets to `dist/release`.
It ends by installing them with the plugin store code of the pinned CLIProxyAPI version, the same check CI runs on every push.

For another platform, build with CGO on that platform:

```bash
go build -trimpath -buildmode=c-shared -o expiring-quota-first.so .
```

## Release

Push a tag `v<version>`, for example `v0.1.0`.
CI builds the assets, publishes the GitHub release, and then installs that release through CLIProxyAPI's plugin store code as a final check.

## Configure

Both `enabled` flags are needed:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    expiring-quota-first:
      enabled: true
      priority: 10
      mode: shadow
```

It starts in `shadow` mode, which only records what it would pick.
Read `recent_decisions` in `plugins/expiring-quota-first-data/state.json`, and once they look right set `mode: active` to let it route.
Switching mode drops its session bindings.

The other fields are optional:

- `ttl` is the idle time before a session forgets its account, `168h` by default.
- `rebind_after` is how long a session's account may be unavailable before the session moves, `15m` by default.
- `min_remaining` is the weekly percent left below which new sessions avoid an account, `5` by default.
- `state_file` is where bindings and reset times are kept, `plugins/expiring-quota-first-data/state.json` by default.

## License

[MIT](LICENSE)
