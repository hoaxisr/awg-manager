package mcp

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type dnsRewriteIndexIn struct {
	Index          int    `json:"index" jsonschema:"zero-based index from list_singbox_dns_rewrites; read the list again before each edit"`
	ExpectRevision string `json:"expectRevision" jsonschema:"required ordered-list revision from list_singbox_dns_rewrites; any list change invalidates it, so read again before each update or deletion"`
}

type updateDNSRewriteIn struct {
	dnsRewriteIndexIn
	SingboxDNSRewriteInput
}

// NormalizeSingboxDNSRewrite bounds and validates model-controlled inputs.
// The production adapter and portable fake use the same input grammar.
func NormalizeSingboxDNSRewrite(in SingboxDNSRewriteInput) (SingboxDNSRewriteInput, error) {
	p := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(in.Pattern)), ".")
	if p == "" || len(p) > 253 || strings.Count(p, "*") > 1 {
		return in, fmt.Errorf("pattern must be an ASCII domain or a domain glob, at most 253 characters")
	}
	for _, c := range in.Pattern {
		if c < 32 || c > 126 {
			return in, fmt.Errorf("pattern must use printable ASCII (punycode for IDNs), without control characters")
		}
	}
	for _, label := range strings.Split(p, ".") {
		if len(label) == 0 || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return in, fmt.Errorf("pattern has an invalid domain label")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '*') {
				return in, fmt.Errorf("pattern must use ASCII domain labels (punycode for IDNs)")
			}
		}
	}
	if i := strings.IndexByte(p, '*'); i >= 0 && !strings.Contains(p[i+1:], ".") {
		return in, fmt.Errorf("a glob needs a domain suffix after the asterisk, e.g. *.example.com")
	}
	if len(in.IPs) == 0 || len(in.IPs) > 64 {
		return in, fmt.Errorf("ips must contain between 1 and 64 addresses")
	}
	ips := make([]string, 0, len(in.IPs))
	for _, raw := range in.IPs {
		addr, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err != nil || addr.Zone() != "" {
			return in, fmt.Errorf("ips must contain IPv4 or IPv6 addresses without ports, prefixes or zones")
		}
		ips = append(ips, addr.Unmap().String())
	}
	return SingboxDNSRewriteInput{Pattern: p, IPs: ips}, nil
}

func requireDNSRewriteRevision(index int, revision string) error {
	if index < 0 {
		return fmt.Errorf("index must not be negative")
	}
	if data, err := hex.DecodeString(revision); err != nil || len(data) != 32 {
		return fmt.Errorf("expectRevision is required: use the entry's revision from list_singbox_dns_rewrites")
	}
	return nil
}

func dnsRewriteToolResult(out SingboxDNSRewriteResult, err error) (*mcp.CallToolResult, SingboxDNSRewriteResult, error) {
	if err != nil {
		return nil, out, err
	}
	out.Rewrites = orEmpty(out.Rewrites)
	text := "DNS Rewrite change saved immediately, not staged. This does not verify that the running DNS engine is serving it."
	if !out.StateKnown {
		text += " The saved state could not be read back. Call list_singbox_dns_rewrites before retrying; retrying an add may create a duplicate."
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, out, nil
}

func registerDNSRewriteTools(s *mcp.Server, d Deps) {
	const immediate = " Saves immediately to the dedicated DNS Rewrite slot, NOT the shared routing draft; apply_singbox_staging is not needed. Works in fakeip-tun too. Preset-managed entries cannot be edited."
	mcp.AddTool(s, &mcp.Tool{
		Name:        "add_singbox_dns_rewrite",
		Description: "Append a user DNS Rewrite: sing-box answers matching domains with fixed IP addresses before other DNS rules. Earlier entries take priority. A single-family entry suppresses answers of the other family. Repeating an add can create a duplicate; read the list before retrying." + immediate,
		Annotations: safeWrite("Add sing-box DNS Rewrite", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SingboxDNSRewriteInput) (*mcp.CallToolResult, SingboxDNSRewriteResult, error) {
		in, err := NormalizeSingboxDNSRewrite(in)
		if err != nil {
			return nil, SingboxDNSRewriteResult{}, err
		}
		out, err := d.AddSingboxDNSRewrite(ctx, in)
		return dnsRewriteToolResult(out, err)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "update_singbox_dns_rewrite",
		Description: "Replace a user DNS Rewrite WHOLE: pass its complete new pattern and IP list. Read list_singbox_dns_rewrites first and pass index and revision; a changed or shifted entry is refused without saving." + immediate,
		Annotations: safeWrite("Update sing-box DNS Rewrite", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in updateDNSRewriteIn) (*mcp.CallToolResult, SingboxDNSRewriteResult, error) {
		if err := requireDNSRewriteRevision(in.Index, in.ExpectRevision); err != nil {
			return nil, SingboxDNSRewriteResult{}, err
		}
		r, err := NormalizeSingboxDNSRewrite(in.SingboxDNSRewriteInput)
		if err != nil {
			return nil, SingboxDNSRewriteResult{}, err
		}
		out, err := d.UpdateSingboxDNSRewrite(ctx, in.Index, in.ExpectRevision, r)
		return dnsRewriteToolResult(out, err)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "delete_singbox_dns_rewrite",
		Description: "Delete a user DNS Rewrite. Read list_singbox_dns_rewrites first and pass index and revision; a changed or shifted entry is refused without saving. Deletion shifts later positions: read the list again before another edit." + immediate,
		Annotations: destructiveWrite("Delete sing-box DNS Rewrite", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in dnsRewriteIndexIn) (*mcp.CallToolResult, SingboxDNSRewriteResult, error) {
		if err := requireDNSRewriteRevision(in.Index, in.ExpectRevision); err != nil {
			return nil, SingboxDNSRewriteResult{}, err
		}
		out, err := d.DeleteSingboxDNSRewrite(ctx, in.Index, in.ExpectRevision)
		return dnsRewriteToolResult(out, err)
	})
}
