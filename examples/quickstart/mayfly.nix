# The headline path: one Nix file describes the whole run. From this directory,
#   nix run github:conor-sometimes/mayfly
# reads this mayfly.nix, boots each distro, installs the packages, copies the
# files, runs the test, and tears every VM down.
#
# This is a mayfly module: it sets options.mayfly.*, type-checked against the
# schema. This sample tests the distro itself (no binary under test): the named
# packages install, a file lands, and the check passes. Add
#   subject.binary = ./foo;  subject.unit = ./foo.service;
# to test your own daemon.
{ ... }:
{
  mayfly = {
    distros.ubuntu-2404.enable = true;

    subject = {
      packages = [ "curl" "python3" ];
      files = { "/etc/quickstart/hello.txt" = ./hello.txt; };
    };

    test = ./check.sh;
  };
}
