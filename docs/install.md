# Install

Which CLIProxyAPI versions and platforms a release of Expiring Quota First fits, and how to install one by hand. The plugin store install is in the [README](../README.md#try-it).

## Compatibility

Each release is built for the CLIProxyAPI version pinned in `go.mod` at its tag, and its release notes name that version. The README's badge shows the version pinned on `main`, which the next release will use.
When the plugin interface of your CLIProxyAPI differs, CLIProxyAPI logs `pluginhost: failed to load plugin` and routes with its own rules.

## Platforms

Releases cover linux/amd64 and linux/arm64.
They are built on the same Debian version as the official `eceasy/cli-proxy-api` image, pinned in `build.sh`, so they need no newer glibc than that image has.
The arm64 library is cross-compiled on amd64 and checked for its machine type and entry symbol only.
No arm64 host has loaded it in the release pipeline, so whether it loads there is unconfirmed, and an issue reporting either outcome is welcome.
For another platform, [build it yourself](develop.md).

## Through the management API

The plugin store page of the management console is one front for CLIProxyAPI's management API, which can be called directly once the README's store source is in place:

```bash
curl -X POST -H "Authorization: Bearer <management key>" \
  http://<cliproxyapi>/v8/management/plugins/store/expiring-quota-first/install
```

## By hand

The store install, through the console or the API, checks the download against the release's `checksums.txt`, installs it into CLIProxyAPI's `plugins` folder and loads it without a restart.
To do the same by hand:

1. Download `expiring-quota-first_<version>_<os>_<arch>.zip` from the [latest release](https://github.com/stavarengo/CLIProxyAPI-Expiring-Quota-First/releases/latest) and check it against `checksums.txt`.
2. Put the `expiring-quota-first.so` inside the zip into the `plugins` folder. Keep the file name: CLIProxyAPI takes the plugin id from it.
3. Add the plugin's config block as in the [README](../README.md#try-it), including the plugin's own `enabled: true`, which only the store install writes for you.
4. Restart CLIProxyAPI.
