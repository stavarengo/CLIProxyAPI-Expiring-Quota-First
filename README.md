# CLIProxyAPI Expiring Quota First

A scheduler plugin for [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) that sends each new session to the account whose weekly quota resets soonest, and keeps a session on its account, across restarts too.
It skips accounts that are used up or logged out, and avoids accounts with little weekly quota left while another one is ready.

It learns quota from the response headers CLIProxyAPI already receives, so it holds no tokens and makes no network calls.
A per-model limit is only known after the first request that limit refuses.

## Build

The plugin targets CLIProxyAPI v8.0.33, the version pinned in `go.mod`.
CLIProxyAPI drops a plugin built for another ABI and keeps its own routing without logging why.
Build with CGO on the same platform as CLIProxyAPI, and keep the file name, because the plugin id is the file's basename.

```bash
go build -trimpath -buildmode=c-shared -o expiring-quota-first.so .
```

Copy `expiring-quota-first.so` into CLIProxyAPI's `plugins` folder and restart it.

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
