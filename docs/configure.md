# Configure

Every setting of Expiring Quota First with its default, what the three timing and threshold settings do, and where the plugin keeps its state.

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

- `ttl`: how long a session keeps its account after its last request. After that, its next request counts as a new session.
- `rebind_after`: how long a bound account may be missing from the accounts CLIProxyAPI offers before the session moves to another. Until then, each request fails over to the best account for that request only, and the session keeps its binding.
- `min_remaining`: the percent of the weekly quota under which an account gets no new sessions while another account is ready.

## Where the state lives

In the official image the default `state_file` lands in `/CLIProxyAPI/plugins`.
The Docker Compose file that ships with CLIProxyAPI mounts that folder from the host, so the state survives a recreated container.
If you start the container another way, or set another `dir`, choose a `state_file` on a mounted path. Otherwise bindings and learned quota start over with each new container.
[State file](state-file.md) says what is in it.

## Changing settings

The plugin picks up config changes live and keeps what it learned about each account's quota.
Switching `mode` drops the session bindings, since a shadow binding names the account the plugin would have picked, not the one CLIProxyAPI used.
