#!/usr/bin/env bash
# The protocol demonstration. It reports a and c cleanly, reports an UNPLANNED id
# (x) which the CLI refuses with a `protocol` error, and never reports b. The
# engine renders a=PASS c=PASS b=ERROR(not reported) protocol=ERROR(unplanned):
# the two kinds of engine-synthesised error in one grid. A real consumer reports
# every planned id exactly once; this one is wrong on purpose.
set -u

mayfly info demo "the mayfly result protocol"
mayfly pass a "reported cleanly"
mayfly pass c
mayfly pass x "this id is not in the plan" || true # unplanned -> protocol error, exit 2
# b is intentionally never reported.
exit 0
