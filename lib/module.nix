# The mayfly options schema. A run is a module: the user sets options.mayfly.*
# (in a mayfly.nix, or in their own flake), evalModules type-checks it, and the
# runner executes the result. A wrong field fails at eval with a clear message.
# The README options reference is generated from this schema.
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
      start = mkOption { type = types.bool; default = true; description = "Enable and start the subject's units by default (a daemon). Set false to install but not start (banken's oneshot, driven by `scan`)."; };
      enable = mkOption { type = types.listOf types.str; default = [ ]; description = "Extra units to `systemctl enable --now` beyond the subject's own."; };
      setup = mkOption { type = types.nullOr types.path; default = null; description = "A script run once as root after files land."; };
      ocsf = mkOption { type = types.str; default = ""; description = "Glob of OCSF jsonl findings this subject writes (for detect checks)."; };
      scan = mkOption { type = types.listOf types.str; default = [ ]; description = "Command to drive a batch subject once (banken's oneshot scan)."; };
    };
  };
in
{
  options.mayfly = {
    distros = mkOption {
      type = types.attrsOf (types.submodule distroModule);
      default = { };
      example = { ubuntu-2404.enable = true; };
      description = "Distros to boot. Enable a shipped one (`ubuntu-2404.enable = true`) or add a custom entry (image + pm + packages).";
    };

    subject = mkOption {
      type = types.nullOr (types.submodule subjectModule);
      default = null;
      description = "The single thing under test. Use `subjects` for more than one.";
    };

    subjects = mkOption {
      type = types.attrsOf (types.submodule subjectModule);
      default = { };
      description = "Named subjects, provisioned in order (banken's matrix passes mimi and banken).";
    };

    test = mkOption {
      type = types.nullOr (types.either types.path types.str);
      default = null;
      description = "A script run in-guest after the subject is up; exit 0 is a pass (the simple tier).";
    };

    checks = mkOption {
      type = types.attrsOf types.attrs;
      default = { };
      description = "Structured checks (detect, script, sandbox, lsm). Free-form per check; `when` may be a predicate on the distro.";
    };

    sshUser = mkOption {
      type = types.str;
      default = "matrix";
      description = "The sudo user the harness provisions and connects as.";
    };

    parallel = mkOption {
      type = types.int;
      default = 0;
      description = "Max distros booted at once; 0 bounds by host CPU and RAM.";
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
