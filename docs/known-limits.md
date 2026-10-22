# Known limits

What Expiring Quota First does not see or decide, for judging whether it fits your setup before installing it.

- Requests for providers other than Claude and Codex, and routes that mix providers, are left to CLIProxyAPI's own routing.
- A request with no session id is left to CLIProxyAPI. The plugin looks for the session identity CLIProxyAPI attaches to the request, then for the session id headers CLIProxyAPI itself accepts.
- An account's quota is unknown until it has served a request, and a per-model weekly limit only after a request to that model has shown it. Accounts that have served a request but report no weekly quota, such as plain API keys, rank last among ready accounts.
- The plugin reads Codex's weekly window from the `X-Codex-Primary-*` and `X-Codex-Secondary-*` headers as the account's one main limit. Whether those headers describe another limit when `X-Codex-Active-Limit` names a per-model one is unconfirmed. If the headers do, the account's record blends two limits. Codex's own per-model weekly limits, such as the one on its Spark model, come under their own header prefix, which the plugin does not read, so an account out of Spark keeps getting Spark sessions.
- Of Claude's per-model weekly limits the plugin reads only Fable's, from the response headers named `7d_oi`. Claude Code's code names limits for other models too, but no capture shows which responses would carry them, so the plugin ignores them until a capture does.
- The plugin ranks only the accounts CLIProxyAPI offers. CLIProxyAPI lets you set a priority on each credential, and offers a plugin only the accounts of the highest priority that still has a usable one, unless the plugin declares the `scheduler_across_priorities` capability, which this one does not. So a lower-priority account with an earlier reset waits until every higher-priority account is unusable. Without priorities, every account is offered.
- Under CLIProxyAPI's Home mode, the control-plane mode its `-home-jwt` flag turns on, every pick goes through Home and no scheduler plugin is asked. The plugin loads but never decides.
- Releases are Linux only; [Install](install.md) says which platforms, and [Develop](develop.md) how to build for another.
