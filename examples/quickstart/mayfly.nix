# The headline path: one Nix file describes the whole run. From this directory,
#   nix run github:conor-sometimes/mayfly
# reads this mayfly.nix, boots each distro, installs the packages, copies the
# files, runs the test, and tears every VM down.
#
# A mayfly.nix is a type-checked module: it sets distros to boot, an `install`
# block (what lands on each guest), and a `test`. This sample tests the distro
# itself (nothing under test): the named packages install, a file lands, and the
# check passes. Add
#   install.binary = ./foo;  install.unit = ./foo.service;
# to test your own daemon.
{ ... }:
{
  distros.ubuntu-2404.enable = true;

  install = {
    packages = [ "curl" "python3" ];
    files."/etc/quickstart/hello.txt" = ./hello.txt;
  };

  test = ./check.sh;
}
