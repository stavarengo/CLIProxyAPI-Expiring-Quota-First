# State file

What Expiring Quota First writes to its state file, for reading its picks in shadow mode or checking what it knows about an account. The file's path is the `state_file` setting in [Configure](configure.md).

JSON, written shortly after something changes and on shutdown, with times in RFC 3339 UTC:

- `bindings`: one entry per session, keyed by a short hash of the session id so no raw id lands on disk. It names the `auth_id` the session is on, when the session was `last_seen`, and `missing_since` when that account stopped being offered.
- `accounts`: what the response headers have said about each account, keyed by the same credential id as `auth_id`, the one the management API lists. `weekly_reset` and `weekly_used`, a fraction from 0 to 1, describe the current weekly quota. `full_until` is set while the weekly quota is known to be used up, and `model_full_until` says the same per model family, such as `fable`. `auth_failed` is the status, 401 or 403, of the last rejected login with no success since, received at `auth_failed_at`; a rejected login keeps the account unusable for 30 minutes, after which it is the last resort until a request on it succeeds. `served` is true once the account has answered a request.
- `models`: each route alias seen in usage, such as a `best` that maps to Fable, to the upstream model it ran as. So a per-model limit also covers picks that name the alias.
- `recent_decisions`: the last 200 picks, oldest first. Each has the time, the hashed session, the model, the `auth_id`, a short `reason` such as `new session; soonest weekly reset`, and `applied`. `applied` is true only when the plugin routed the request, so never in shadow mode.
- `mode`: the mode the bindings were made in, since a mode switch drops them.

A file that cannot be parsed is renamed to `state.json.corrupt` and the plugin starts fresh.
