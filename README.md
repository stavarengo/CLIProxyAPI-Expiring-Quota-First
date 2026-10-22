# CLIProxyAPI Expiring Quota First

[![Release](https://github.com/stavarengo/CLIProxyAPI-Expiring-Quota-First/actions/workflows/ci.yml/badge.svg)](https://github.com/stavarengo/CLIProxyAPI-Expiring-Quota-First/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/stavarengo/CLIProxyAPI-Expiring-Quota-First)](https://github.com/stavarengo/CLIProxyAPI-Expiring-Quota-First/releases/latest)
[![Built for CLIProxyAPI](https://img.shields.io/badge/dynamic/regex?url=https%3A%2F%2Fraw.githubusercontent.com%2Fstavarengo%2FCLIProxyAPI-Expiring-Quota-First%2Fmain%2Fgo.mod&search=CLIProxyAPI%2Fv8%20(v%5B0-9.%5D%2B)&replace=%241&label=built%20for%20CLIProxyAPI)](https://github.com/router-for-me/CLIProxyAPI)
[![License: MIT](https://img.shields.io/github/license/stavarengo/CLIProxyAPI-Expiring-Quota-First)](LICENSE)

Spend your Claude and Codex weekly quotas before they reset.

A scheduler plugin for [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI), for anyone running two or more Claude or Codex subscriptions behind it.
Without it, one account hits its weekly limit while another still has half its week unused, and on reset day that half is gone.
With it, each new session, meaning each new Claude Code or Codex CLI conversation, goes to the account whose quota resets soonest, so the quota about to expire is the one you burn.

## What you get

- Sessions are sticky. A conversation stays on its account, across CLIProxyAPI restarts too, and moves only when that account is used up, its login is rejected, or CLIProxyAPI stops offering it.
- New sessions avoid accounts that are used up, nearly used up, or whose login was rejected, as long as another account is ready.
- Claude's separate weekly limit on its top model is tracked as well, so a session for that model avoids an account that is out of it. As of 2026-10 that model is Fable, on Max plans.
- A Claude Code subagent gets the same account as the conversation that started it.
- It starts in shadow mode: it records each pick it would have made, with the reason, in a [state file](docs/state-file.md), and routes nothing until you switch it to active. Until then CLIProxyAPI routes as before.
- The plugin's data stays on your machine. It learns quota from the rate-limit headers CLIProxyAPI already receives, makes no network calls of its own, and writes no keys or raw session ids to disk.

## How it picks an account

```mermaid
flowchart TD
    R([A conversation sends a request]) --> B{Does this conversation<br>already have an account?}
    B -- yes --> O{Is that account still<br>available, with quota left?}
    O -- yes --> K[Keep using it]
    O -- "no, or it has been<br>gone for a while" --> N
    B -- no --> N[Choose the best account]
    N --> RANK
    subgraph RANK[Ranked, best first]
        direction LR
        G1[1 Ready<br>has quota left, or<br>not checked yet] ~~~ G2[2 Unconfirmed<br>should have reset by now,<br>not seen working since] ~~~ G3[3 Low<br>almost out of quota] ~~~ G4[4 Unusable<br>out of quota, or<br>login just failed]
    end
    RANK --> P[Use it for the rest<br>of this conversation]
    RANK -. no usable account .-> C[CLIProxyAPI decides, as it<br>would without the plugin]

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
Among ready accounts the soonest weekly reset wins, except that an account whose reset time is still unknown is tried first, so the plugin learns it.
A session keeps its account until it has been idle for a week, by default.
[Configure](docs/configure.md) has the timing and threshold settings, and [Known limits](docs/known-limits.md) what the plugin cannot see.

## Try it

Releases are for Linux and built for the CLIProxyAPI version in the badge above; [Install](docs/install.md) has the details.

1. Add this repository's `registry.json` as a plugin store source in CLIProxyAPI's `config.yaml`. Skip this step when the plugin store page of your management console already lists the plugin:

   ```yaml
   plugins:
     store-sources:
       - "https://raw.githubusercontent.com/stavarengo/CLIProxyAPI-Expiring-Quota-First/main/registry.json"
   ```

2. Install `Expiring Quota First` from the plugin store page of your management console.

3. Turn plugins on and start in `shadow` mode. The plugin's own `enabled` is already there from the store install; the global `plugins.enabled` is off while missing, so add it. The block should read:

   ```yaml
   plugins:
     enabled: true
     configs:
       expiring-quota-first:
         enabled: true
         mode: shadow
   ```

4. After some traffic, read the picks in the [state file](docs/state-file.md). Once they are the accounts you would have chosen, set `mode: active`.

## Read more

- [Install](docs/install.md): which CLIProxyAPI versions and platforms a release fits, the management API call, and installing by hand.
- [Configure](docs/configure.md): every setting with its default, and where the state lives.
- [State file](docs/state-file.md): what the plugin records, and how to read its picks.
- [Known limits](docs/known-limits.md): what the plugin does not see or decide.
- [Develop](docs/develop.md): tests, reproducing a release build, and releasing.

## License

[MIT](LICENSE)
