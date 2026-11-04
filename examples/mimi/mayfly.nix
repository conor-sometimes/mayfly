# Does mimi, banken's eBPF process-telemetry sensor, attach and emit on a real
# distro kernel (not the NixOS VM it is tested on at home)? mimi loads a
# tracepoint probe via CO-RE off the running kernel's own BTF and streams exec
# events as OCSF-shaped JSON (the mimi/2 schema banken ingests) to a sink file.
#
# This matrix installs the fully-static musl ship binary as a hardened systemd
# unit, boots Ubuntu 24.04, and asserts the probe loads off Ubuntu's kernel and
# a fresh canary exec is captured end to end. Ubuntu only for now (the walking
# skeleton); other distros are one `.enable = true` each once this is green.
#
# A mayfly module. The example flake (flake.nix) injects `mimi` (its repo as a
# flake input) and `pkgs`. See README.md: this needs the mimi repo reachable.
{ mimi, pkgs, ... }:
{
  distros.ubuntu-2404.enable = true;

  install = {
    # The fully-static musl build (copy-and-run on non-NixOS): the bytes mimi
    # ships, not the gnu/NixOS build. The probe is embedded in the binary.
    binary = "${mimi.packages.x86_64-linux.mimi-musl}/bin/mimi";
    # A fixed path the unit's ExecStart can name (the default install path is
    # derived from the install's name, not "mimi" for an unnamed single install).
    installPath = "/usr/local/bin/mimi";
    # A long-running daemon, not a one-shot: mayfly enables and starts it
    # (install.start defaults true), then the test runs.
    unit = ./mimi.service;
  };

  # Exit 0 = the unit stayed up, the probe loaded off Ubuntu's BTF, and the
  # canary exec was captured as a mimi/2 OCSF event. See check.sh.
  test = ./check.sh;
}
