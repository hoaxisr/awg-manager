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

func registerHydraRouteTools(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_hydraroute_geo_files",
		Description: "Geo data files (geoip, geosite) — where geoip:/geosite: tags in sing-box rules come from. With path, the tags of that file instead, paged.",
		Annotations: readOnly("List geo files"),
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
		Description: "Download geo files again — one, or every file awg-manager downloaded — and wait. " +
			"Network work: it can take a while, and a slow source can run past the call's deadline.",
		Annotations: safeWrite("Update geo files", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in geoUpdateIn) (*mcp.CallToolResult, HydraRouteGeoUpdate, error) {
		if in.Path != "" && (!geoPathShape.MatchString(in.Path) || strings.Contains(in.Path, "..")) {
			return nil, HydraRouteGeoUpdate{}, fmt.Errorf("path must be a geo file's path from list_hydraroute_geo_files")
		}
		out, err := d.UpdateHydraRouteGeoFiles(ctx, in.Path)
		return nil, out, err
	})
}
