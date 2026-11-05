# The result protocol, by example

A consumer whose entrypoint talks the [result protocol](../../docs/protocol.md)
through the `mayfly` CLI instead of a single exit code. It is deliberately wrong,
to show the engine's two synthesised errors:

- it reports `a` and `c` cleanly (PASS);
- it reports `x`, which is not in the plan -> the CLI refuses it and the engine
  raises a `protocol` ERROR (spec 5.2);
- it never reports `b` -> the engine raises `b`'s ERROR (unreported, spec 5.1).

So `nix run .#distro-ubuntu-2404` renders `a=PASS c=PASS b=ERROR protocol=ERROR`.
The same outcome is asserted without a VM in `tests/conformance`.
