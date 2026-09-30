package transport

// RCI payload builders for the JSON-POST form of /show commands.
//
// Background: NDMS RCI accepts GET /show/<path>/<name> or POST /rci/ with
// {"show":{"<path>":{"name":"<name>"}}}. Both forms work for "flat" names
// like "Wireguard0" or "OpkgTun10", but the GET form fails with 404 when
// the name contains a slash — NDMS treats it as another path segment.
// Slashed names are not exotic: every VLAN ("GigabitEthernet0/Vlan2"),
// numbered switch-group port ("GigabitEthernet0/3") and Wi-Fi
// access-point ("WifiMaster0/AccessPoint0") has one. The POST form
// embeds the name in the JSON body, where the RCI parser handles it
// correctly regardless of contained slashes.
//
// These builders produce the request body. The HTTP layer is the
// existing Client.Post.

// ShowQuery builds an RCI POST payload for any /show/<path>/... command.
// The target name is carried inside the JSON body so slashes don't trip
// the URL parser. Caller supplies the command tree under "show" as a
// slice of segments; args is merged into the innermost object.
//
//	ShowQuery([]string{"interface"}, map[string]any{"name": "GigabitEthernet0/Vlan2"})
//	  => {"show":{"interface":{"name":"GigabitEthernet0/Vlan2"}}}
//
//	ShowQuery([]string{"interface"}, map[string]any{"name": "Wireguard0", "details": "yes"})
//	  => {"show":{"interface":{"name":"Wireguard0","details":"yes"}}}
//
// Empty args yields the bare command (useful for listings):
//
//	ShowQuery([]string{"interface"}, nil)
//	  => {"show":{"interface":{}}}
//
// Nested sub-commands (e.g. /show/interface/<name>/wireguard/peer) are
// not modelled here — their exact body shape depends on NDMS conventions
// and must be probed per-endpoint before adding a wrapper.
func ShowQuery(path []string, args map[string]any) any {
	leaf := make(map[string]any, len(args))
	for k, v := range args {
		leaf[k] = v
	}
	var cur any = leaf
	for i := len(path) - 1; i >= 0; i-- {
		cur = map[string]any{path[i]: cur}
	}
	return map[string]any{"show": cur}
}

// ShowInterface is shorthand for the by-name interface fetch — by far
// the most common single-interface RCI query in this codebase.
//
//	ShowInterface("GigabitEthernet0/Vlan2", nil)
//	  => {"show":{"interface":{"name":"GigabitEthernet0/Vlan2"}}}
//
//	ShowInterface("Wireguard0", map[string]any{"details": "yes"})
//	  => {"show":{"interface":{"name":"Wireguard0","details":"yes"}}}
//
// extra keys win over the auto-set "name" — pass extra["name"] = "..."
// to override, though there's no current reason to.
func ShowInterface(name string, extra map[string]any) any {
	args := map[string]any{"name": name}
	for k, v := range extra {
		args[k] = v
	}
	return ShowQuery([]string{"interface"}, args)
}
