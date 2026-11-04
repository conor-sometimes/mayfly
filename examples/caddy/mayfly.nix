# A community demo: mayfly testing a famous third-party static binary, unchanged.
#
# Caddy ships a single static Go binary per release. This fetches that exact
# release artifact (no building, no patching), installs it as a hardened systemd
# unit serving a tiny site, and asserts it answers on every distro. It is the
# whole mayfly pitch on someone else's software: "does the binary you ship
# actually run everywhere?"
#
# Not affiliated with Caddy or the Caddy project. Nothing here is submitted
# upstream; it is a demonstration only.
{ mayfly, pkgs, ... }:
let
  # fetchRelease unpacks a project's own release archive and returns the one
  # binary inside it, so the matrix tests the bytes Caddy ships.
  caddy = mayfly.lib.fetchRelease pkgs {
    url = "https://github.com/caddyserver/caddy/releases/download/v2.11.8/caddy_2.11.8_linux_amd64.tar.gz";
    sha256 = "f032f8805e4df2ce5e1a8a624495c91e9ff763d8ff3ceddadd4784a07c472f40";
    bin = "caddy";
  };
in
{
  distros = {
    ubuntu-2404.enable = true;
    debian-13.enable = true;
    fedora-44.enable = true;
    rocky-9.enable = true;
    opensuse-leap-16.enable = true;
  };
  concurrency = 1;

  install = {
    binary = "${caddy}/bin/caddy";
    installPath = "/usr/local/bin/caddy";
    files."/etc/caddy/Caddyfile" = ./Caddyfile;
    unit = ./caddy.service;
    # curl ships on every cloud image the check runs on, so nothing to install.
  };

  # Exit 0 = the unit came up and the shipped binary served the expected page.
  test = ./check.sh;
}
