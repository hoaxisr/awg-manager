package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

var keendnsLabelRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

var reservedKeenDNSLabels = map[string]bool{
	"no":       true,
	"default":  true,
	"admin":    true,
	"setup":    true,
	"my":       true,
	"keenetic": true,
}

var defaultForbiddenPublicationPorts = map[int]bool{
	22:   true, // SSH
	79:   true, // Keenetic RCI internal HTTP
	80:   true, // Keenetic HTTP web configurator
	443:  true, // Keenetic HTTPS web configurator
	1099: true, // Local mixed SOCKS5/HTTP inbound
	2222: true, // AWGM UI/API default port
	2398: true, // Telegram mtg backend
	8086: true, // Telegram admin port
}

var (
	ErrInvalidKeenDNSLabel      = errors.New("invalid KeenDNS label")
	ErrForbiddenPublicationPort = errors.New("forbidden KeenDNS publication target port")
	ErrInvalidPublicationTarget = errors.New("invalid KeenDNS publication target")
)

// ValidateKeenDNSLabel validates a 4th-level KeenDNS subdomain label (e.g., "connect", "vless", "tg").
func ValidateKeenDNSLabel(label string) error {
	if label == "" {
		return fmt.Errorf("%w: label cannot be empty", ErrInvalidKeenDNSLabel)
	}
	if len(label) > 63 {
		return fmt.Errorf("%w: label exceeds 63 characters", ErrInvalidKeenDNSLabel)
	}
	if strings.ContainsAny(label, ".*:/\\ \t\r\n") {
		return fmt.Errorf("%w: label must be a single DNS label without dots, wildcards, paths, ports, or whitespace", ErrInvalidKeenDNSLabel)
	}
	if !keendnsLabelRegex.MatchString(label) {
		return fmt.Errorf("%w: label %q must consist of lowercase ASCII letters, digits, and hyphens, starting and ending with a letter or digit", ErrInvalidKeenDNSLabel, label)
	}
	if reservedKeenDNSLabels[label] {
		return fmt.Errorf("%w: label %q is reserved", ErrInvalidKeenDNSLabel, label)
	}
	return nil
}

// HTTPProxyPublicationSpec describes a validated KeenDNS 4th-level reverse-proxy publication (`ip http proxy <label>`).
type HTTPProxyPublicationSpec struct {
	Label          string `json:"label"`
	UpstreamProto  string `json:"upstream_proto,omitempty"`
	UpstreamHost   string `json:"upstream_host"`
	UpstreamPort   int    `json:"upstream_port"`
	AllowedPorts   []int  `json:"allowed_ports,omitempty"`
	ForbiddenPorts []int  `json:"forbidden_ports,omitempty"`
	BaseRawJSON    string `json:"base_raw_json,omitempty"`
}

// ValidatePublicationSpec enforces strict security guardrails on KeenDNS publications:
// - Only valid 4th-level labels
// - Only local router loopback ("127.0.0.1") over HTTP
// - Never admin UI/API ports (2222 / custom Server.Port), router web configurator (80/443/79), SSH (22), or internal proxy ports
// - Only explicitly allowed ingress ports (defaults to 9009 if AllowedPorts is empty)
func ValidatePublicationSpec(spec HTTPProxyPublicationSpec) error {
	if err := ValidateKeenDNSLabel(spec.Label); err != nil {
		return err
	}
	proto := strings.ToLower(strings.TrimSpace(spec.UpstreamProto))
	if proto == "" {
		proto = "http"
	}
	if proto != "http" {
		return fmt.Errorf("%w: upstream proto must be http, got %q", ErrInvalidPublicationTarget, spec.UpstreamProto)
	}
	host := strings.TrimSpace(spec.UpstreamHost)
	if host != "127.0.0.1" {
		return fmt.Errorf("%w: upstream host must be 127.0.0.1, got %q", ErrInvalidPublicationTarget, spec.UpstreamHost)
	}
	if spec.UpstreamPort <= 0 || spec.UpstreamPort > 65535 {
		return fmt.Errorf("%w: upstream port %d out of range", ErrInvalidPublicationTarget, spec.UpstreamPort)
	}
	if defaultForbiddenPublicationPorts[spec.UpstreamPort] {
		return fmt.Errorf("%w: port %d is a protected router or admin port and cannot be published", ErrForbiddenPublicationPort, spec.UpstreamPort)
	}
	for _, fp := range spec.ForbiddenPorts {
		if fp > 0 && spec.UpstreamPort == fp {
			return fmt.Errorf("%w: port %d is a protected admin port and cannot be published", ErrForbiddenPublicationPort, spec.UpstreamPort)
		}
	}

	allowed := spec.AllowedPorts
	if len(allowed) == 0 {
		allowed = []int{9009}
	}
	portAllowed := false
	for _, ap := range allowed {
		if ap > 0 && spec.UpstreamPort == ap {
			portAllowed = true
			break
		}
	}
	if !portAllowed {
		return fmt.Errorf("%w: port %d is not in the allowed ingress ports list", ErrForbiddenPublicationPort, spec.UpstreamPort)
	}
	return nil
}

// HTTPProxyCommands manages KeenDNS 4th-level web application proxy publications (`ip.http.proxy`).
type HTTPProxyCommands struct {
	poster  Poster
	save    *SaveCoordinator
	queries *query.Queries
}

func NewHTTPProxyCommands(p Poster, s *SaveCoordinator, q *query.Queries) *HTTPProxyCommands {
	return &HTTPProxyCommands{poster: p, save: s, queries: q}
}

func (c *HTTPProxyCommands) invalidators() []func() {
	if c == nil || c.queries == nil {
		return nil
	}
	var out []func()
	if c.queries.RunningConfig != nil {
		out = append(out, c.queries.RunningConfig.InvalidateAll)
	}
	if c.queries.HTTPProxy != nil {
		out = append(out, c.queries.HTTPProxy.InvalidateAll)
	}
	return out
}

func (c *HTTPProxyCommands) postAndInvalidate(ctx context.Context, payload any, opDesc string) error {
	err := PostChecked(ctx, c.poster, payload, opDesc, nil)
	for _, fn := range c.invalidators() {
		if fn != nil {
			fn()
		}
	}
	return err
}

func mergeNestedMap(root map[string]any, key string, updates map[string]any, overwriteExisting bool) {
	nested, ok := root[key].(map[string]any)
	if !ok || nested == nil {
		nested = make(map[string]any, len(updates))
	} else {
		copied := make(map[string]any, len(nested)+len(updates))
		for k, v := range nested {
			copied[k] = v
		}
		nested = copied
	}
	for k, v := range updates {
		if _, exists := nested[k]; !exists || overwriteExisting {
			nested[k] = v
		}
	}
	root[key] = nested
}

// UpsertPublication creates or updates `ip http proxy <label>` for a validated AWGM ingress target.
// If BaseRawJSON is provided, unknown top-level and nested fields from the existing entry are preserved.
func (c *HTTPProxyCommands) UpsertPublication(ctx context.Context, spec HTTPProxyPublicationSpec) error {
	if c == nil || c.poster == nil {
		return errors.New("http proxy commands not initialized")
	}
	if err := ValidatePublicationSpec(spec); err != nil {
		return err
	}

	entryMap := make(map[string]any)
	hasBase := false
	if trimmed := strings.TrimSpace(spec.BaseRawJSON); trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &entryMap); err != nil {
			return fmt.Errorf("decode base raw json for %s: %w", spec.Label, err)
		}
		hasBase = true
	}

	mergeNestedMap(entryMap, "upstream", map[string]any{
		"proto":    "http",
		"upstream": "127.0.0.1",
		"port":     strconv.Itoa(spec.UpstreamPort),
	}, true)
	mergeNestedMap(entryMap, "domain", map[string]any{
		"ndns": true,
	}, true)
	mergeNestedMap(entryMap, "ssl", map[string]any{
		"redirect": true,
	}, !hasBase)
	mergeNestedMap(entryMap, "security-level", map[string]any{
		"public": true,
	}, true)
	if _, exists := entryMap["preserve-host"]; !exists || !hasBase {
		entryMap["preserve-host"] = true
	}
	mergeNestedMap(entryMap, "auth", map[string]any{
		"no": true,
	}, true)

	payload := map[string]any{
		"ip": map[string]any{
			"http": map[string]any{
				"proxy": map[string]any{
					spec.Label: entryMap,
				},
			},
		},
	}
	return c.postAndInvalidate(ctx, payload, "ip http proxy "+spec.Label)
}

// DeletePublication removes `ip http proxy <label>` (`no ip http proxy <label>`).
func (c *HTTPProxyCommands) DeletePublication(ctx context.Context, label string) error {
	if c == nil || c.poster == nil {
		return errors.New("http proxy commands not initialized")
	}
	if err := ValidateKeenDNSLabel(label); err != nil {
		return err
	}
	payload := map[string]any{
		"ip": map[string]any{
			"http": map[string]any{
				"proxy": map[string]any{
					label: map[string]any{"no": true},
				},
			},
		},
	}
	return c.postAndInvalidate(ctx, payload, "no ip http proxy "+label)
}

// RestorePublicationRaw restores a previously captured raw JSON snapshot for `ip http proxy <label>`.
func (c *HTTPProxyCommands) RestorePublicationRaw(ctx context.Context, label string, rawJSON string) error {
	if c == nil || c.poster == nil {
		return errors.New("http proxy commands not initialized")
	}
	if err := ValidateKeenDNSLabel(label); err != nil {
		return err
	}
	trimmed := strings.TrimSpace(rawJSON)
	if trimmed == "" {
		return errors.New("rawJSON snapshot cannot be empty")
	}
	var check map[string]any
	if err := json.Unmarshal([]byte(trimmed), &check); err != nil {
		return fmt.Errorf("invalid rawJSON snapshot for %s: %w", label, err)
	}
	payload := map[string]any{
		"ip": map[string]any{
			"http": map[string]any{
				"proxy": map[string]any{
					label: check,
				},
			},
		},
	}
	return c.postAndInvalidate(ctx, payload, "restore ip http proxy "+label)
}

// SaveConfig flushes running-config to startup-config (`system.configuration.save`).
func (c *HTTPProxyCommands) SaveConfig(ctx context.Context) error {
	if c == nil || c.save == nil {
		return errors.New("save coordinator not initialized")
	}
	return c.save.Flush(ctx)
}
