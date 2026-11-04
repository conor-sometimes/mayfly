// Command hello is the smoke matrix's subject: a tiny static binary that proves
// the whole lifecycle (boot, provision, run, teardown) without any real tool
// under test. `nix run .#matrix` installs it and runs smoke/smoke.sh against it.
package main

import "fmt"

func main() {
	fmt.Println("mayfly hello")
}
