# A consumer that exercises the result protocol directly (docs/protocol.md): its
# entrypoint reports through the `mayfly` CLI rather than a simple exit code. It
# deliberately reports an UNPLANNED id and never reports a planned one, so the
# grid shows the two kinds of engine-synthesised ERROR: an unplanned id becomes a
# `protocol` error (spec 5.2), an unreported id becomes that id's error (5.1). The
# behaviour is asserted purely in tests/conformance; this is the runnable demo.
{ ... }:
{
  distros.ubuntu-2404.enable = true;
  entrypoint = ./entrypoint.sh;
  checks = {
    a = { };
    b = { }; # deliberately never reported -> ERROR "not reported"
    c = { };
  };
}
