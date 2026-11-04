# The banken family dogfood: does the hitomi static binary, as shipped, detect
# correctly on each real distro? hitomi is banken's YARA content-scanner sibling
# (a musl static binary). This matrix installs it with its rule set and the
# EICAR accuracy suite, and the test asserts 100% true positives and 0% false
# positives on every distro.
#
# A mayfly module. The example flake (flake.nix) injects `hitomi` (its repo as a
# flake input) and `pkgs`. See README.md: this needs the hitomi repo reachable.
{ hitomi, pkgs, ... }:
let
  # hitomi ships its rules and its EICAR suite under testdata/. Carry each as a
  # tar the guest extracts (the engine stages single files; setup.sh unpacks).
  rulesTar = pkgs.runCommand "hitomi-rules.tar" { } "tar -C ${hitomi}/testdata -cf $out rules";
  suiteTar = pkgs.runCommand "hitomi-suite.tar" { } "tar -C ${hitomi}/testdata -cf $out suite";
in
{
  distros = {
    ubuntu-2404.enable = true;
    fedora-44.enable = true;
    rocky-9.enable = true;
  };

  install = {
    binary = "${hitomi.packages.x86_64-linux.hitomi-musl}/bin/hitomi";
    files = {
      "/opt/hitomi/rules.tar" = rulesTar;
      "/opt/hitomi/suite.tar" = suiteTar;
    };
    setup = ./setup.sh; # unpacks the tars into /etc/hitomi/rules and /opt/suite
  };

  # Exit 0 on 100% TP / 0% FP; any miss or false positive fails the distro.
  test = ./run-suite.sh;
}
