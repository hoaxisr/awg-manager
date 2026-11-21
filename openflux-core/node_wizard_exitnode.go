//go:build exitnode

package main

import (
	"fmt"
	"io"
)

// The exit-node build (deploy/node-install.sh installs it) leaves the
// desktop wizard out: its pinned installer would otherwise be part of the
// very binary whose hashes that installer pins.
func runNodeWizard(_ io.Reader, out io.Writer) int {
	fmt.Fprintln(out, `{"ok":false,"error":"--node-wizard is not part of the exit-node build"}`)
	return 2
}
