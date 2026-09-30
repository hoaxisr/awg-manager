package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/aiassistant"
	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/connections"
	"github.com/hoaxisr/awg-manager/internal/diagnostics"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	singboxrouter "github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

var diagnosticURLPattern = regexp.MustCompile(`(?i)https?://[^\s]+`)
var diagnosticSecretPattern = regexp.MustCompile(`(?i)\b(token|api[_-]?key|password|secret|authorization)\b\s*[:=]\s*[^\s,;]+`)
var diagnosticBearerPattern = regexp.MustCompile(`(?i)\bbearer\s+[^\s,;]+`)

func safeDiagnosticError(value string) string {
	value = safeDiagnosticText(value)
	if len(value) > 500 {
		value = value[:500] + "…"
	}
	return value
}

func safeDiagnosticText(value string) string {
	value = diagnosticURLPattern.ReplaceAllString(strings.TrimSpace(value), "[redacted-url]")
	value = diagnosticBearerPattern.ReplaceAllString(value, "Bearer [redacted]")
	value = diagnosticSecretPattern.ReplaceAllString(value, "$1=[redacted]")
	return logging.SanitizeLogText(value)
}

func (s *Server) aiToolSources(connectionSource func() *connections.Service, diagnosticRunner aiassistant.DiagnosticsRunner, systemTools *api.SystemToolsHandler) aiassistant.ToolSources {
	return aiassistant.ToolSources{
		SystemSnapshot: func(context.Context) (any, error) {
			if systemTools == nil {
				return nil, errors.New("system tools are unavailable")
			}
			return systemTools.AssistantSystemSnapshot()
		},
		SystemServices: func(context.Context) (any, error) {
			if systemTools == nil {
				return nil, errors.New("system tools are unavailable")
			}
			return systemTools.AssistantServices()
		},
		SystemPorts: func(context.Context) (any, error) {
			if systemTools == nil {
				return nil, errors.New("system tools are unavailable")
			}
			return systemTools.AssistantPorts()
		},
		SystemPackages: func(_ context.Context, kind, query string) (any, error) {
			if systemTools == nil {
				return nil, errors.New("system tools are unavailable")
			}
			return systemTools.AssistantPackages(kind, query)
		},
		SystemFilesList: func(_ context.Context, path string) (any, error) {
			if systemTools == nil {
				return nil, errors.New("system tools are unavailable")
			}
			return systemTools.AssistantFilesList(path)
		},
		SystemFileRead: func(_ context.Context, path string) (any, error) {
			if systemTools == nil {
				return nil, errors.New("system tools are unavailable")
			}
			return systemTools.AssistantFileRead(path)
		},
		FullDiagnostics: func(ctx context.Context) (any, error) {
			if diagnosticRunner == nil {
				return nil, errors.New("diagnostics runner is unavailable")
			}
			events, err := diagnosticRunner.RunWithStream(ctx, diagnostics.RunOptions{IncludeRestart: false})
			if err != nil {
				return nil, err
			}
			for range events {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			raw, err := diagnosticRunner.Result()
			if err != nil {
				return nil, err
			}
			var report any
			if err := json.Unmarshal(raw, &report); err != nil {
				return nil, fmt.Errorf("decode diagnostics report: %w", err)
			}
			return report, nil
		},
		EngineStatus: func(ctx context.Context) (any, error) {
			result := map[string]any{}
			if s.settings != nil {
				if settings, err := s.settings.Load(); err == nil {
					result["routing"] = map[string]any{
						"enabled":        settings.SingboxRouter.Enabled,
						"selectedEngine": "singbox",
					}
				} else {
					result["settingsError"] = safeDiagnosticError(err.Error())
				}
			}
			if s.singboxOp != nil {
				status := s.singboxOp.GetStatus(ctx)
				result["singbox"] = map[string]any{
					"installed": status.Installed, "running": status.Running, "pid": status.PID,
					"version": status.Version, "tunnelCount": status.TunnelCount,
					"proxyComponent": status.ProxyComponent, "lastError": safeDiagnosticError(status.LastError),
				}
			}
			return result, nil
		},
		Tunnels: func(ctx context.Context) (any, error) {
			result := make([]map[string]any, 0)
			if s.tunnelService != nil {
				items, err := s.tunnelService.List(ctx)
				if err == nil {
					for _, item := range items {
						result = append(result, map[string]any{
							"id": item.ID, "name": item.Name, "state": item.State,
							"enabled": item.Enabled, "autoStart": item.AutoStart,
							"backend": item.Backend, "interface": item.InterfaceName,
							"defaultRoute": item.DefaultRoute,
							"type":         "awg-manager",
						})
					}
				}
			}
			if s.systemTunnelService != nil {
				sysTuns, err := s.systemTunnelService.List(ctx)
				if err == nil {
					for _, item := range sysTuns {
						handshakeStr := ""
						rxBytes := int64(0)
						txBytes := int64(0)
						peerOnline := false
						if item.Peer != nil {
							handshakeStr = item.Peer.LastHandshake
							rxBytes = item.Peer.RxBytes
							txBytes = item.Peer.TxBytes
							peerOnline = item.Peer.Online
						}
						result = append(result, map[string]any{
							"id": item.ID, "name": item.Description, "state": item.Status,
							"enabled": item.Connected, "backend": "keenetic-ndms",
							"interface": item.InterfaceName, "address": item.Address,
							"uptimeSeconds": item.Uptime,
							"peerOnline":    peerOnline,
							"lastHandshake": handshakeStr,
							"rxBytes":       rxBytes, "txBytes": txBytes,
							"type": "system-tunnel",
						})
					}
				}
			}
			return result, nil
		},
		TunnelStatus: func(ctx context.Context, id string) (any, error) {
			if strings.TrimSpace(id) == "" {
				return nil, errors.New("tunnel id is required")
			}
			if s.systemTunnelService != nil {
				sysTuns, err := s.systemTunnelService.List(ctx)
				if err == nil {
					for _, item := range sysTuns {
						if item.ID == id || strings.EqualFold(item.InterfaceName, id) || strings.EqualFold(item.Description, id) {
							handshakeStr := ""
							rxBytes := int64(0)
							txBytes := int64(0)
							online := false
							endpoint := ""
							via := ""
							if item.Peer != nil {
								handshakeStr = item.Peer.LastHandshake
								rxBytes = item.Peer.RxBytes
								txBytes = item.Peer.TxBytes
								online = item.Peer.Online
								endpoint = item.Peer.Endpoint
								via = item.Peer.Via
							}
							return map[string]any{
								"id": item.ID, "name": item.Description, "state": item.Status,
								"enabled": item.Connected, "backend": "keenetic-ndms",
								"interface": item.InterfaceName, "address": item.Address,
								"uptimeSeconds": item.Uptime,
								"components": map[string]any{
									"interfaceUp": item.Status == "up",
									"connected":   item.Connected,
									"hasPeer":     item.Peer != nil,
									"peerOnline":  online,
								},
								"lastHandshake": handshakeStr,
								"rxBytes":       rxBytes,
								"txBytes":       txBytes,
								"peerEndpoint":  endpoint,
								"peerVia":       via,
							}, nil
						}
					}
				}
			}
			if s.tunnelService == nil {
				return nil, errors.New("tunnel service is unavailable")
			}
			item, err := s.tunnelService.Get(ctx, id)
			if err != nil {
				return nil, err
			}
			state := item.StateInfo
			stateError := ""
			if state.Error != nil {
				stateError = safeDiagnosticError(state.Error.Error())
			}
			connectivityInfo := map[string]any{
				"status": "not_running",
			}
			if s.testingService != nil && item.State == tunnel.StateRunning {
				res, _ := s.testingService.CheckConnectivity(ctx, item.ID)
				if res != nil {
					indicator := "зелёный (связь в норме)"
					if !res.Connected {
						indicator = "красный (Нет связи в UI)"
					}
					connectivityInfo = map[string]any{
						"connected":   res.Connected,
						"reason":      res.Reason,
						"latencyMs":   res.Latency,
						"uiIndicator": indicator,
					}
				}
			}
			return map[string]any{
				"id": item.ID, "name": item.Name, "state": item.State,
				"enabled": item.Enabled, "backend": item.Backend,
				"interface": item.InterfaceName, "defaultRoute": item.DefaultRoute,
				"components": map[string]any{
					"registered": state.OpkgTunExists, "interfaceUp": state.InterfaceUp,
					"processRunning": state.ProcessRunning, "pid": state.ProcessPID,
					"hasPeer": state.HasPeer, "hasHandshake": state.HasHandshake,
				},
				"connectivity":  connectivityInfo,
				"lastHandshake": state.LastHandshake, "rxBytes": state.RxBytes,
				"txBytes": state.TxBytes, "peerVia": state.PeerVia,
				"details": safeDiagnosticError(state.Details), "error": stateError,
			}, nil
		},
		Subscriptions: func(context.Context) (any, error) {
			result := map[string]any{"singbox": []any{}}
			if s.subscriptionHandler != nil {
				items := s.subscriptionHandler.AssistantStatus()
				safeItems := make([]map[string]any, 0, len(items))
				for _, item := range items {
					safeItems = append(safeItems, map[string]any{
						"id": item.ID, "name": item.Label, "enabled": item.Enabled,
						"mode": item.Mode, "memberCount": item.MemberCount,
						"activeMember": item.ActiveMember, "lastFetched": item.LastFetched,
						"lastError": safeDiagnosticError(item.LastError),
					})
				}
				result["singbox"] = safeItems
			}
			return result, nil
		},
		Connections: func(ctx context.Context, search string, limit int) (any, error) {
			service := connectionSource()
			if service == nil {
				return nil, errors.New("connections service is unavailable")
			}
			return safeConnectionSearch(ctx, service, search, limit)
		},
		ExplainClient: func(ctx context.Context, client string) (any, error) {
			client = strings.TrimSpace(client)
			if client == "" {
				return nil, errors.New("client IP or name is required")
			}
			result := map[string]any{"query": client}
			var resolvedIP string
			var policyName string
			var partialErrors []string
			if s.accessPolicyService != nil {
				devices, err := s.accessPolicyService.ListDevices(ctx)
				if err != nil {
					partialErrors = append(partialErrors, safeDiagnosticError(err.Error()))
				} else {
					for _, device := range devices {
						if device.IP == client || strings.EqualFold(device.Name, client) || strings.EqualFold(device.Hostname, client) {
							resolvedIP, policyName = device.IP, device.Policy
							result["device"] = map[string]any{"ip": device.IP, "name": device.Name, "hostname": device.Hostname, "active": device.Active, "policy": device.Policy}
							break
						}
					}
				}
				if policyName != "" {
					policies, err := s.accessPolicyService.List(ctx)
					if err != nil {
						partialErrors = append(partialErrors, safeDiagnosticError(err.Error()))
					} else {
						for _, policy := range policies {
							if policy.Name == policyName {
								result["policy"] = policy
								break
							}
						}
					}
				}
			}
			if resolvedIP == "" {
				resolvedIP = client
			}
			if s.clientRouteService != nil {
				routes, err := s.clientRouteService.List()
				if err != nil {
					partialErrors = append(partialErrors, safeDiagnosticError(err.Error()))
				} else {
					matches := make([]any, 0)
					for _, route := range routes {
						if route.ClientIP == resolvedIP || strings.EqualFold(route.ClientHostname, client) {
							matches = append(matches, route)
						}
					}
					result["clientRoutes"] = matches
				}
			}
			if service := connectionSource(); service != nil {
				live, err := safeConnectionSearch(ctx, service, resolvedIP, 30)
				if err != nil {
					partialErrors = append(partialErrors, safeDiagnosticError(err.Error()))
				} else {
					result["live"] = live
				}
			}
			if s.settings != nil {
				if settings, err := s.settings.Load(); err == nil {
					result["routingEngine"] = "singbox"
					result["routingEnabled"] = settings.SingboxRouter.Enabled
				}
			}
			if len(partialErrors) > 0 {
				result["partialErrors"] = partialErrors
			}
			return result, nil
		},
		Logs: func(_ context.Context, bucketName, group, level string, limit int) (any, error) {
			if s.loggingService == nil {
				return nil, errors.New("logging service is unavailable")
			}
			bucket, err := allowedLogBucket(bucketName)
			if err != nil {
				return nil, err
			}
			if !allowedLogGroup(bucket, group) {
				return nil, fmt.Errorf("log group %q is not allowed for bucket %q", group, bucketName)
			}
			if !allowedLogLevel(level) {
				return nil, fmt.Errorf("log level %q is not allowed", level)
			}
			entries, total := s.loggingService.GetLogs(bucket, group, "", level, time.Time{}, limit, 0)
			result := make([]map[string]any, 0, len(entries))
			for _, entry := range entries {
				result = append(result, map[string]any{
					"time": entry.Timestamp, "level": entry.Level, "group": entry.Group,
					"subgroup": entry.Subgroup, "action": safeDiagnosticText(entry.Action),
					"target": safeDiagnosticText(entry.Target), "message": safeDiagnosticText(entry.Message),
					"repeats": entry.Repeats, "lastSeen": entry.LastSeen,
				})
			}
			return map[string]any{"totalMatched": total, "entries": result}, nil
		},
		ProxyGroups: func(ctx context.Context, engine string) (any, error) {
			resolved, client, err := s.aiClashClient(engine)
			if err != nil {
				return nil, err
			}
			proxies, err := client.GetProxies()
			if err != nil {
				return nil, err
			}
			names := make([]string, 0, len(proxies))
			for name, proxy := range proxies {
				if len(proxy.All) > 0 {
					names = append(names, name)
				}
			}
			sort.Strings(names)
			groups := make([]map[string]any, 0, len(names))
			for _, name := range names {
				proxy := proxies[name]
				members := proxy.All
				if len(members) > 100 {
					members = members[:100]
				}
				lastDelay := 0
				if len(proxy.History) > 0 {
					lastDelay = proxy.History[len(proxy.History)-1].Delay
				}
				groups = append(groups, map[string]any{
					"name": name, "type": proxy.Type, "selected": proxy.Now,
					"memberCount": len(proxy.All), "members": members, "lastDelayMs": lastDelay,
				})
			}
			return map[string]any{"engine": resolved, "groupCount": len(groups), "groups": groups}, nil
		},
		InspectRule: func(ctx context.Context, destination string, port int, protocol string) (any, error) {
			return s.inspectAIRule(ctx, destination, port, protocol)
		},
		TestOutbound: func(ctx context.Context, engine, name string) (any, error) {
			name = strings.TrimSpace(name)
			if name == "" || len(name) > 200 || strings.ContainsAny(name, "\r\n") {
				return nil, errors.New("valid outbound name is required")
			}
			resolved, client, err := s.aiClashClient(engine)
			if err != nil {
				return nil, err
			}
			delay, err := client.TestDelay(ctx, name, "https://www.gstatic.com/generate_204", 5*time.Second)
			if err != nil {
				return nil, err
			}
			return map[string]any{"engine": resolved, "outbound": name, "available": true, "delayMs": delay}, nil
		},
		ExplainDNS: func(ctx context.Context, domain, sourceIP, queryType string) (any, error) {
			domain = strings.TrimSpace(domain)
			sourceIP = strings.TrimSpace(sourceIP)
			queryType = strings.ToUpper(strings.TrimSpace(queryType))
			if domain == "" || len(domain) > 253 || !safeInspectDestination(domain) || net.ParseIP(domain) != nil {
				return nil, errors.New("valid DNS domain is required")
			}
			if sourceIP != "" && net.ParseIP(sourceIP) == nil {
				return nil, errors.New("sourceIp must be an IP literal")
			}
			if queryType != "" && queryType != "A" && queryType != "AAAA" {
				return nil, errors.New("queryType must be A or AAAA")
			}
			if _, err := s.selectedAIEngine("auto"); err != nil {
				return nil, err
			}
			if s.singboxRouterHandler == nil || s.singboxRouterHandler.Service() == nil {
				return nil, errors.New("sing-box DNS inspector is unavailable")
			}
			result, err := s.singboxRouterHandler.Service().InspectDNS(ctx, singboxrouter.InspectDNSInput{Domain: domain, SourceIP: sourceIP, QueryType: queryType})
			if err != nil {
				return nil, err
			}
			return map[string]any{"engine": "singbox", "result": result}, nil
		},
		ExplainConn: func(ctx context.Context, source string, sourcePort int, destination string, destinationPort int, protocol string) (any, error) {
			service := connectionSource()
			if service == nil {
				return nil, errors.New("connections service is unavailable")
			}
			source, destination = strings.TrimSpace(source), strings.TrimSpace(destination)
			protocol = strings.ToLower(strings.TrimSpace(protocol))
			if net.ParseIP(source) == nil || net.ParseIP(destination) == nil {
				return nil, errors.New("source and destination must be IP literals")
			}
			if protocol != "tcp" && protocol != "udp" && protocol != "icmp" {
				return nil, errors.New("protocol must be tcp, udp or icmp")
			}
			response, err := service.List(ctx, connections.ListParams{Search: source, Limit: 500, Tunnel: "all", Protocol: protocol})
			if err != nil {
				return nil, err
			}
			var match *connections.Connection
			for i := range response.Connections {
				item := &response.Connections[i]
				if item.Src == source && item.Dst == destination && (sourcePort == 0 || item.SrcPort == sourcePort) && (destinationPort == 0 || item.DstPort == destinationPort) {
					match = item
					break
				}
			}
			if match == nil {
				return nil, errors.New("matching live connection was not found")
			}
			inspectDestination := match.Dst
			if len(match.Rules) > 0 && strings.TrimSpace(match.Rules[0].FQDN) != "" {
				inspectDestination = match.Rules[0].FQDN
			}
			inspectProtocol := match.Protocol
			if inspectProtocol == "icmp" {
				inspectProtocol = ""
			}
			ruleResult, ruleErr := s.inspectAIRule(ctx, inspectDestination, match.DstPort, inspectProtocol)
			result := map[string]any{"connection": safeConnectionItem(*match), "inspectedDestination": inspectDestination}
			if ruleErr != nil {
				result["ruleError"] = safeDiagnosticError(ruleErr.Error())
			} else {
				result["ruleInspection"] = ruleResult
			}
			return result, nil
		},
	}
}

func (s *Server) selectedAIEngine(requested string) (string, error) {
	requested = strings.ToLower(strings.TrimSpace(requested))
	switch requested {
	case "", "auto", "sing-box", "singbox":
		return "singbox", nil
	default:
		return "", fmt.Errorf("proxy engine %q is unsupported", requested)
	}
}

func (s *Server) aiClashClient(requested string) (string, *singbox.ClashClient, error) {
	engine, err := s.selectedAIEngine(requested)
	if err != nil {
		return "", nil, err
	}
	if s.singboxOp == nil || s.singboxOp.Clash() == nil {
		return "", nil, errors.New("sing-box Clash API is unavailable")
	}
	return engine, s.singboxOp.Clash(), nil
}

func (s *Server) inspectAIRule(ctx context.Context, destination string, port int, protocol string) (any, error) {
	destination = strings.TrimSpace(destination)
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if destination == "" || len(destination) > 253 || !safeInspectDestination(destination) {
		return nil, errors.New("destination must be a domain or IP literal")
	}
	if port < 0 || port > 65535 {
		return nil, errors.New("port must be between 0 and 65535")
	}
	if protocol != "" && protocol != "tcp" && protocol != "udp" {
		return nil, errors.New("protocol must be tcp or udp")
	}
	engine, err := s.selectedAIEngine("auto")
	if err != nil {
		return nil, err
	}
	if s.singboxRouterHandler == nil || s.singboxRouterHandler.Service() == nil {
		return nil, errors.New("sing-box router inspector is unavailable")
	}
	result, err := s.singboxRouterHandler.Service().Inspect(ctx, singboxrouter.InspectInput{Domain: destination, Port: port, Protocol: protocol})
	if err != nil {
		return nil, err
	}
	return map[string]any{"engine": engine, "result": result}, nil
}

var inspectDestinationPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

func safeInspectDestination(value string) bool {
	return inspectDestinationPattern.MatchString(value) && !strings.Contains(value, "..")
}

func safeConnectionSearch(ctx context.Context, service *connections.Service, search string, limit int) (any, error) {
	response, err := service.List(ctx, connections.ListParams{Search: search, Limit: limit, Tunnel: "all", Protocol: "all"})
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(response.Connections))
	for _, item := range response.Connections {
		items = append(items, safeConnectionItem(item))
	}
	return map[string]any{"stats": response.Stats, "totalMatched": response.Pagination.Total, "connections": items}, nil
}

func safeConnectionItem(item connections.Connection) map[string]any {
	return map[string]any{
		"protocol": item.Protocol, "source": item.Src, "sourcePort": item.SrcPort,
		"destination": item.Dst, "destinationPort": item.DstPort,
		"state": item.State, "routeClass": item.RouteClass,
		"interface": item.Interface, "tunnelId": item.TunnelID,
		"tunnelName": item.TunnelName, "clientName": item.ClientName,
		"bytesIn": item.BytesIn, "bytesOut": item.BytesOut, "rules": item.Rules,
	}
}

func allowedLogBucket(value string) (logging.Bucket, error) {
	switch value {
	case "app":
		return logging.BucketApp, nil
	case "singbox":
		return logging.BucketSingbox, nil
	default:
		return "", fmt.Errorf("log bucket %q is not allowed", value)
	}
}

func allowedLogGroup(bucket logging.Bucket, value string) bool {
	if value == "" {
		return true
	}
	if bucket == logging.BucketSingbox {
		return value == logging.GroupSingbox
	}
	return value == logging.GroupTunnel || value == logging.GroupRouting || value == logging.GroupServer || value == logging.GroupSystem
}

func allowedLogLevel(value string) bool {
	switch value {
	case "", "error", "warn", "info", "full", "debug":
		return true
	default:
		return false
	}
}
