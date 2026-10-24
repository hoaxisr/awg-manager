package mcp

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MaxGeoTagsInOutput caps one page of a geo file's tags.
const MaxGeoTagsInOutput = 200

var geoPathShape = regexp.MustCompile(`^/[A-Za-z0-9/_.-]{1,255}$`)
var geoTagShape = regexp.MustCompile(`^(geoip:)?[A-Za-z0-9_.!@-]{1,64}$`)

type hydraControlIn struct {
	Action string `json:"action" jsonschema:"start, stop or restart"`
}

type hydraSettingsIn struct {
	AutoStart           *bool `json:"autoStart,omitempty" jsonschema:"start HydraRoute Neo on router boot"`
	ClearIPSet          *bool `json:"clearIpset,omitempty" jsonschema:"empty the ipsets when it starts"`
	IpsetTimeoutEnabled *bool `json:"ipsetTimeoutEnabled,omitempty" jsonschema:"remove addresses from the ipsets after ipsetTimeout"`
	IpsetTimeout        *int  `json:"ipsetTimeout,omitempty" jsonschema:"seconds, 1-2147483 (21600 = 6 hours); used while ipsetTimeoutEnabled is on"`
	IpsetMaxElem        *int  `json:"ipsetMaxElem,omitempty" jsonschema:"entries one ipset can hold, 1-16777216 (default 65536); a geoip tag larger than this is disabled"`
	ConntrackFlush      *bool `json:"conntrackFlush,omitempty" jsonschema:"flush conntrack when a new address appears"`
}

type geoFilesIn struct {
	Path   string `json:"path,omitempty" jsonschema:"a geo file's path from this list: return its tags instead"`
	Offset int    `json:"offset,omitempty" jsonschema:"index of the first tag returned, with path"`
}

type geoFilesOut struct {
	Files []HydraRouteGeoFile `json:"files,omitempty"`
	Tags  *HydraRouteGeoTags  `json:"tags,omitempty"`
}

type geoUpdateIn struct {
	Path string `json:"path,omitempty" jsonschema:"one geo file's path from list_hydraroute_geo_files; omit to update every downloaded file"`
}

type geoUpdateOut struct {
	Updated int  `json:"updated"`
	Partial bool `json:"partial" jsonschema:"true when some files were updated and others failed; the journal says which"`
}

type disabledTagIn struct {
	Name string `json:"name" jsonschema:"disabled geoip tag from get_hydraroute_status (ru or geoip:ru)"`
}

type removedTagOut struct {
	Removed string `json:"removed"`
}

func registerHydraRouteTools(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "get_hydraroute_status",
		Description: "HydraRoute Neo: whether the daemon is installed and running, how full its ipsets are, the geoip tags it could not use, and its settings. " +
			"Its rule groups are DNS routes with hr: ids (list_dns_routes).",
		Annotations: readOnly("HydraRoute status"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, HydraRouteStatus, error) {
		out, err := d.HydraRouteStatus(ctx)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "control_hydraroute",
		Description: "Start, stop or restart the HydraRoute Neo daemon. While it is stopped its rule groups route nothing.",
		Annotations: safeWrite("Control HydraRoute", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in hydraControlIn) (*mcp.CallToolResult, HydraRouteStatus, error) {
		action := strings.ToLower(strings.TrimSpace(in.Action))
		if action != "start" && action != "stop" && action != "restart" {
			return nil, HydraRouteStatus{}, fmt.Errorf("action must be start, stop or restart")
		}
		out, err := d.ControlHydraRoute(ctx, action)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "set_hydraroute_settings",
		Description: "Change HydraRoute Neo's settings (hrneo.conf): autostart, ipset size and timeout, clearing, conntrack flush. Omitted fields are kept. The daemon restarts to read them. " +
			"Global routing and DirectRoute are not changed here: global routing overwrites the router's policies.",
		Annotations: safeWrite("Set HydraRoute settings", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in hydraSettingsIn) (*mcp.CallToolResult, HydraRouteStatus, error) {
		p := HydraRouteSettingsPatch{AutoStart: in.AutoStart, ClearIPSet: in.ClearIPSet, IpsetTimeoutEnabled: in.IpsetTimeoutEnabled, ConntrackFlush: in.ConntrackFlush, IpsetTimeout: in.IpsetTimeout, IpsetMaxElem: in.IpsetMaxElem}
		if p == (HydraRouteSettingsPatch{}) {
			return nil, HydraRouteStatus{}, fmt.Errorf("nothing to change: pass at least one setting")
		}
		if in.IpsetMaxElem != nil && (*in.IpsetMaxElem < 1 || *in.IpsetMaxElem > 16777216) {
			return nil, HydraRouteStatus{}, fmt.Errorf("ipsetMaxElem must be between 1 and 16777216")
		}
		if in.IpsetTimeout != nil && (*in.IpsetTimeout < 1 || *in.IpsetTimeout > 2147483) {
			return nil, HydraRouteStatus{}, fmt.Errorf("ipsetTimeout must be between 1 and 2147483 seconds")
		}
		out, err := d.UpdateHydraRouteSettings(ctx, p)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_hydraroute_geo_files",
		Description: "Geo data files of HydraRoute Neo (geoip, geosite) — where geoip:/geosite: tags in its rules come from. With path, the tags of that file instead, paged.",
		Annotations: readOnly("List HydraRoute geo files"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in geoFilesIn) (*mcp.CallToolResult, geoFilesOut, error) {
		if in.Path == "" {
			files, err := d.ListHydraRouteGeoFiles(ctx)
			return nil, geoFilesOut{Files: orEmpty(files)}, err
		}
		if !geoPathShape.MatchString(in.Path) || strings.Contains(in.Path, "..") {
			return nil, geoFilesOut{}, fmt.Errorf("path must be a geo file's path from list_hydraroute_geo_files")
		}
		if in.Offset < 0 {
			return nil, geoFilesOut{}, fmt.Errorf("offset must not be negative")
		}
		tags, err := d.HydraRouteGeoTags(ctx, in.Path, in.Offset)
		if err != nil {
			return nil, geoFilesOut{}, err
		}
		tags.Tags = orEmpty(tags.Tags)
		return nil, geoFilesOut{Tags: &tags}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "update_hydraroute_geo_files",
		Description: "Download HydraRoute Neo's geo files again — one, or every file awg-manager downloaded — and wait; the daemon restarts to read them. " +
			"Network work: it can take a while, and a slow source can run past the call's deadline.",
		Annotations: safeWrite("Update HydraRoute geo files", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in geoUpdateIn) (*mcp.CallToolResult, geoUpdateOut, error) {
		if in.Path != "" && (!geoPathShape.MatchString(in.Path) || strings.Contains(in.Path, "..")) {
			return nil, geoUpdateOut{}, fmt.Errorf("path must be a geo file's path from list_hydraroute_geo_files")
		}
		n, partial, err := d.UpdateHydraRouteGeoFiles(ctx, in.Path)
		if err != nil {
			return nil, geoUpdateOut{}, err
		}
		return nil, geoUpdateOut{Updated: n, Partial: partial}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "remove_hydraroute_disabled_tag",
		Description: "Remove a geoip tag from HydraRoute Neo's disabled list (\"impossible to use\"); rules are not changed, and the tag does not come back into a rule by itself — add it to the rule again. " +
			"Every hr: route id changes afterwards: list_dns_routes again before using one.",
		Annotations: safeWrite("Remove HydraRoute disabled tag", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in disabledTagIn) (*mcp.CallToolResult, removedTagOut, error) {
		name := strings.TrimSpace(in.Name)
		if !geoTagShape.MatchString(name) {
			return nil, removedTagOut{}, fmt.Errorf("name must be a geoip tag from get_hydraroute_status")
		}
		if err := d.RemoveHydraRouteDisabledTag(ctx, name); err != nil {
			return nil, removedTagOut{}, err
		}
		return nil, removedTagOut{Removed: name}, nil
	})
}
