# Caddy on the mayfly matrix (community demo)

A demonstration that mayfly tests a famous third-party binary, unchanged. It
fetches Caddy's own release artifact - the single static Go binary the project
ships - installs it as a hardened systemd unit serving a tiny page, and asserts
it answers on each distro. That is the whole mayfly pitch applied to someone
else's software: *does the binary you ship actually run everywhere?*

> Not affiliated with Caddy or the Caddy project. Nothing here is submitted
> upstream - it is a showcase of mayfly's cross-distro use case, nothing more.

## Run it

```sh
nix run .#matrix
```

It boots Ubuntu, Debian, Fedora, Rocky and openSUSE (apt / dnf / zypper), one at
a time, installs `caddy_2.11.8`, starts it as a `DynamicUser` unit on `:8080`,
and checks `curl` gets the expected response on each.

## What it exercises

- `fetchRelease` - the no-build path: the exact bytes Caddy publishes, pinned by
  sha256, not a nixpkgs rebuild.
- a real systemd unit (`caddy.service`) installed, enabled and started per distro.
- a package the check needs (`curl`), installed through each distro's own
  package manager via the generic-name map.

## Updating the version

Bump the `url` and `sha256` in `mayfly.nix` to a newer Caddy release (the
sha256 is `shasum -a 256` of the `_linux_amd64.tar.gz`).
