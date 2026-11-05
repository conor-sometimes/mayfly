# The mayfly options schema. A run is a module: the user writes a mayfly.nix
# (distros, install, and either a `test` script or an `entrypoint` + `checks`
# plan), evalModules type-checks it, and the runner executes the result. A wrong
# field fails at eval with a clear message. The README reference is generated from
# this schema. The in-guest contract is docs/protocol.md (mayfly/1).
{ lib, catalogSet, ... }:
let
  inherit (lib) mkOption mkEnableOption types;

  distroModule = { name, ... }: {
    options = {
      enable = mkEnableOption "booting ${name} in this matrix";
      title = mkOption {
        type = types.str;
        default = name;
        description = "Human name for the generated table.";
      };
      pm = mkOption {
        type = types.nullOr (types.enum [ "dnf" "apt" "zypper" "pacman" ]);
        default = null;
        description = "Package manager. A shipped distro sets it; a custom one must.";
      };
      family = mkOption {
        type = types.str;
        default = "";
        description = "Family tag a check's `when` can test (rhel, debian, suse, arch).";
      };
      image = mkOption {
        type = types.nullOr (types.attrsOf types.str);
        default = null;
        description = "{ url, sha256 } or { url, sha512 } for a custom distro's cloud image.";
      };
      packages = mkOption {
        type = types.attrsOf types.str;
        default = { };
        description = "Generic command name -> package on this distro (custom distro only).";
      };
      boot = {
        mem = mkOption { type = types.int; default = 0; description = "Guest RAM in MiB (0 = 2048)."; };
        cpus = mkOption { type = types.int; default = 0; description = "Guest vCPUs (0 = 2)."; };
        disk = mkOption { type = types.int; default = 0; description = "Overlay size in GiB (0 = max(image, 20))."; };
      };
    };
  };

  subjectModule = {
    options = {
      binary = mkOption {
        type = types.nullOr (types.either types.path types.str);
        default = null;
        description = "A static binary, installed onto PATH (or installPath).";
      };
      installPath = mkOption {
        type = types.str;
        default = "";
        description = "Where to install the binary (default /usr/local/bin/<name>).";
      };
      unit = mkOption { type = types.nullOr types.path; default = null; description = "A systemd unit file, installed and (by default) started."; };
      units = mkOption { type = types.listOf types.path; default = [ ]; description = "Several unit files."; };
      files = mkOption { type = types.attrsOf types.path; default = { }; description = "{ \"/guest/path\" = ./source; } copied verbatim."; };
      packages = mkOption { type = types.listOf types.str; default = [ ]; description = "Generic package names installed first, per distro."; };
      start = mkOption { type = types.bool; default = true; description = "Enable and start this install's units by default (a daemon). Set false to install but not start (a oneshot a check drives)."; };
      enable = mkOption { type = types.listOf types.str; default = [ ]; description = "Extra units to `systemctl enable --now` beyond this install's own."; };
      setup = mkOption { type = types.nullOr types.path; default = null; description = "A script run once as root after files land."; };
    };
  };

  # A plan entry (spec 4.2): the attr name is the check id; the value sets an
  # optional `when` predicate on the distro and the optional `xfail` modifier.
  checkModule = {
    options = {
      when = mkOption {
        type = types.unspecified;
        default = (_: true);
        description = "A predicate on the distro attrs; false excludes this id from that distro's plan (rendered '-').";
      };
      xfail = mkOption {
        type = types.bool;
        default = false;
        description = "Expected-failure modifier (spec 6.2). Not implemented in mayfly/1; setting it is an eval error for now.";
      };
    };
  };
in
{
  options = {
    distros = mkOption {
      type = types.attrsOf (types.submodule distroModule);
      default = { };
      example = { ubuntu-2404.enable = true; };
      description = "Distros to boot. Enable a shipped one (`ubuntu-2404.enable = true`) or add a custom entry (image + pm + packages).";
    };

    install = mkOption {
      type = types.nullOr (types.submodule subjectModule);
      default = null;
      description = "What lands on each guest, and is the thing under test: a binary, packages, units, files, a setup script. Use `installs` to put more than one thing on each guest.";
    };

    installs = mkOption {
      type = types.attrsOf (types.submodule subjectModule);
      default = { };
      description = "Named installs, provisioned in order, when a run needs more than one thing on each guest.";
    };

    test = mkOption {
      type = types.nullOr (types.either types.path types.str);
      default = null;
      description = "The simple tier: one in-guest script, exit 0 is a pass (spec 4.5). Mutually exclusive with `entrypoint`/`checks`.";
    };

    entrypoint = mkOption {
      type = types.nullOr (types.either types.path types.str);
      default = null;
      description = "The one in-guest script the engine runs; it reports each id in `checks` through the `mayfly` CLI (spec 8). Pair with `checks`.";
    };

    checks = mkOption {
      type = types.attrsOf (types.submodule checkModule);
      default = { };
      description = "The plan: one entry per check id the entrypoint will report, each with an optional `when` and `xfail` (spec 4).";
    };

    audit = mkOption {
      type = types.bool;
      default = false;
      description = "Enable auditd, strip `-a task,never`, and load a subject's shipped audit rules after its files land (for detection via `mayfly detect`).";
    };

    testUser = mkOption {
      type = types.str;
      default = "";
      description = "Provision this unprivileged user and open the payload to it; exported as $MAYFLY_TEST_USER (for `mayfly detect --as`).";
    };

    sshUser = mkOption {
      type = types.str;
      default = "matrix";
      description = "The sudo user the harness provisions and connects as.";
    };

    concurrency = mkOption {
      type = types.either (types.enum [ "auto" ]) types.ints.positive;
      default = "auto";
      example = 1;
      description = ''
        How many distros boot at once. "auto" (the default) bounds by host CPU and
        RAM; a positive integer is that many at a time (1 = sequential, essential
        for a wide matrix on a small host where N guests * ~2 GiB each adds up).
      '';
    };

    keepWorkDir = mkOption {
      type = types.bool;
      default = false;
      description = "Keep each VM's work dir (disk overlay, serial log) for inspection.";
    };

    timeouts = {
      boot = mkOption { type = types.str; default = "30m"; description = "Longest one distro may take, boot to report."; };
      ssh = mkOption { type = types.str; default = "8m"; description = "Longest to wait for the guest to answer SSH."; };
      cloudInit = mkOption { type = types.str; default = "10m"; description = "Longest to wait for cloud-init to finish."; };
    };
  };
}
