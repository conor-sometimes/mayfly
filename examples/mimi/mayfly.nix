# Does mimi, banken's eBPF process-telemetry sensor, attach and emit on a real
# distro kernel (not the NixOS VM it is tested on at home)? mimi loads a
# tracepoint probe via CO-RE off the running kernel's own BTF and streams exec
# events as OCSF-shaped JSON (the mimi/2 schema banken ingests) to a sink file.
#
# This matrix installs the fully-static musl ship binary as a hardened systemd
# unit, boots each distro, and asserts the probe loads off that kernel's own BTF
# and a fresh canary exec is captured end to end. Eight distros spanning apt,
# dnf and zypper and kernels from 5.14 (Rocky, RHEL-backported BTF) to the
# newest; several run SELinux enforcing, a real test of the hardened unit's eBPF
# load. The per-kernel tracepoint offset this exercises is exactly what the 5.14
# CO-RE fix (mimi) was found by.
#
# A mayfly module. The example flake (flake.nix) injects `mimi` (its repo as a
# flake input). See README.md: this needs the mimi repo reachable.
{ mimi, ... }:
{
  distros.ubuntu-2404.enable = true;
  distros.ubuntu-2204.enable = true;
  distros.debian-13.enable = true;
  distros.fedora-44.enable = true;
  distros.rocky-9.enable = true;
  distros.alma-10.enable = true;
  distros.amazonlinux-2023.enable = true;
  distros.opensuse-leap-16.enable = true;

  # A wide matrix on one host: boot one distro at a time rather than letting
  # several share the RAM. Drop this (or set "auto") on a big host; raise to 2/3
  # on a host with the RAM for it.
  concurrency = 1;

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

  # Exit 0 = the unit stayed up, the probe loaded off this distro's BTF, and the
  # canary exec was captured as a mimi/2 OCSF event. See check.sh.
  test = ./check.sh;
}
