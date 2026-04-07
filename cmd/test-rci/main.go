package main

import (
	"encoding/json"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/rci"
)

func main() {
	fmt.Println("=== NWG Delete batch ===")
	batch := []any{
		rci.CmdInterfaceDNSClear("Wireguard0"),
		rci.CmdInterfaceDelete("Wireguard0"),
		rci.CmdSave(),
	}
	b, _ := json.MarshalIndent(batch, "", "  ")
	fmt.Println(string(b))

	fmt.Println("\n=== OpkgTun DeleteOpkgTun sequence ===")
	fmt.Println("1. RemoveDefaultRoute:")
	b1, _ := json.MarshalIndent(rci.CmdRemoveDefaultRoute("OpkgTun11"), "", "  ")
	fmt.Println(string(b1))

	fmt.Println("2. Delete interface (name-as-key):")
	del := map[string]any{"interface": map[string]any{"OpkgTun11": map[string]any{"no": true}}}
	b2, _ := json.MarshalIndent(del, "", "  ")
	fmt.Println(string(b2))

	fmt.Println("\n=== CmdInterfaceDelete (name-field) ===")
	b3, _ := json.MarshalIndent(rci.CmdInterfaceDelete("Wireguard0"), "", "  ")
	fmt.Println(string(b3))
}
