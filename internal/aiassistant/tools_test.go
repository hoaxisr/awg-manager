package aiassistant

import (
	"context"
	"errors"
	"strings"
	"testing"

	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
)

type fakeResolver struct {
	addresses []string
	err       error
}

func (f fakeResolver) LookupHost(context.Context, string) ([]string, error) {
	return f.addresses, f.err
}

func TestDetectIntentExtractsDomainFromFreeFormQuestion(t *testing.T) {
	intent := DetectIntent("Проверь, почему https://Ya.Ru/search идёт напрямую")
	if intent.Kind != "domain.inspect" || intent.Entity["domain"] != "ya.ru" {
		t.Fatalf("unexpected intent: %+v", intent)
	}
}

func TestDetectIntentKeepsGreetingInChat(t *testing.T) {
	for _, question := range []string{"Привет!", "Как дела?", "Что ты умеешь?"} {
		if got := DetectIntent(question); got.Kind != "chat" {
			t.Fatalf("DetectIntent(%q) = %+v", question, got)
		}
	}
	if got := DetectIntent("Почему не работает DNS?"); got.Kind != "diagnostics.general" {
		t.Fatalf("diagnostic intent = %+v", got)
	}
}

func TestToolRegistryInspectsResolvedDomainRoutes(t *testing.T) {
	registry := &ToolRegistry{
		resolver: fakeResolver{addresses: []string{"203.0.113.2", "203.0.113.1", "203.0.113.1"}},
		run: func(_ context.Context, name string, args ...string) (*sysexec.Result, error) {
			if name != "/opt/sbin/ip" || len(args) != 3 || args[0] != "route" || args[1] != "get" {
				t.Fatalf("unsafe command shape: %s %v", name, args)
			}
			return &sysexec.Result{Stdout: args[2] + " via 192.0.2.1 dev eth0\n"}, nil
		},
	}

	intent, calls := registry.Plan("Проверь маршрут для example.com")
	if intent.Kind != "domain.inspect" || len(calls) != 1 {
		t.Fatalf("unexpected plan: %+v %+v", intent, calls)
	}
	step := registry.Execute(context.Background(), calls[0])
	if step.Status != "passed" || !step.ReadOnly || len(step.Evidence) != 3 {
		t.Fatalf("unexpected step: %+v", step)
	}
	if !strings.Contains(step.Evidence[0], "203.0.113.1, 203.0.113.2") {
		t.Fatalf("addresses are not sorted/deduplicated: %+v", step.Evidence)
	}
}

func TestToolRegistryReportsDNSFailureWithoutCommands(t *testing.T) {
	registry := &ToolRegistry{
		resolver: fakeResolver{err: errors.New("not found")},
		run: func(context.Context, string, ...string) (*sysexec.Result, error) {
			t.Fatal("command must not run after DNS failure")
			return nil, nil
		},
	}
	_, calls := registry.Plan("открой missing.example")
	step := registry.Execute(context.Background(), calls[0])
	if step.Status != "error" || !strings.Contains(step.Summary, "DNS") {
		t.Fatalf("unexpected failure step: %+v", step)
	}
}

func TestToolRegistryTreatsUnreachableRouteAsWarning(t *testing.T) {
	registry := &ToolRegistry{
		resolver: fakeResolver{addresses: []string{"2001:db8::1"}},
		run: func(context.Context, string, ...string) (*sysexec.Result, error) {
			return &sysexec.Result{Stdout: "unreachable 2001:db8::1 dev lo error -101\n"}, nil
		},
	}
	_, calls := registry.Plan("Проверь example.com")
	step := registry.Execute(context.Background(), calls[0])
	if step.Status != "warning" {
		t.Fatalf("unreachable route status = %q, want warning", step.Status)
	}
}

func TestToolRegistryUsesTypedAWGManagerSources(t *testing.T) {
	var search string
	var limit int
	registry := NewToolRegistryWithSources(ToolSources{
		SystemSnapshot: func(context.Context) (any, error) { return map[string]any{"uptimeSeconds": 10}, nil },
		SystemServices: func(context.Context) (any, error) {
			return []map[string]any{{"script": "S90demo", "running": true}}, nil
		},
		SystemPorts: func(context.Context) (any, error) { return []map[string]any{{"port": 443}}, nil },
		SystemPackages: func(_ context.Context, kind, query string) (any, error) {
			return map[string]any{"kind": kind, "query": query}, nil
		},
		SystemFilesList: func(_ context.Context, path string) (any, error) {
			return map[string]any{"path": path, "entries": []any{}}, nil
		},
		SystemFileRead: func(_ context.Context, path string) (any, error) {
			return map[string]any{"path": path, "content": "port=8080"}, nil
		},
		FullDiagnostics: func(context.Context) (any, error) {
			return map[string]any{"tests": []map[string]any{{"name": "routing", "status": "pass"}}}, nil
		},
		EngineStatus: func(context.Context) (any, error) {
			return map[string]any{"mihomo": map[string]any{"running": true}}, nil
		},
		Tunnels: func(context.Context) (any, error) {
			return []map[string]any{{"id": "awg1", "state": "running"}}, nil
		},
		TunnelStatus: func(_ context.Context, id string) (any, error) {
			return map[string]any{"id": id, "state": "running"}, nil
		},
		Subscriptions: func(context.Context) (any, error) {
			return map[string]any{"mihomo": []map[string]any{{"name": "ABV", "memberCount": 8}}}, nil
		},
		Connections: func(_ context.Context, value string, max int) (any, error) {
			search, limit = value, max
			return map[string]any{"totalMatched": 1}, nil
		},
		ExplainClient: func(_ context.Context, client string) (any, error) {
			return map[string]any{"query": client, "routingEngine": "mihomo"}, nil
		},
		Logs: func(_ context.Context, bucket, group, level string, max int) (any, error) {
			return map[string]any{"bucket": bucket, "group": group, "level": level, "limit": max}, nil
		},
		ProxyGroups: func(_ context.Context, engine string) (any, error) {
			return map[string]any{"engine": engine, "groupCount": 1}, nil
		},
		InspectRule: func(_ context.Context, destination string, port int, protocol string) (any, error) {
			return map[string]any{"destination": destination, "port": port, "protocol": protocol}, nil
		},
		TestOutbound: func(_ context.Context, engine, name string) (any, error) {
			return map[string]any{"engine": engine, "outbound": name, "delayMs": 42}, nil
		},
		ExplainDNS: func(_ context.Context, domain, sourceIP, queryType string) (any, error) {
			return map[string]any{"domain": domain, "sourceIp": sourceIP, "queryType": queryType}, nil
		},
		ExplainConn: func(_ context.Context, source string, sourcePort int, destination string, destinationPort int, protocol string) (any, error) {
			return map[string]any{
				"source": source, "sourcePort": sourcePort,
				"destination": destination, "destinationPort": destinationPort,
				"protocol": protocol,
			}, nil
		},
	})

	for _, name := range []string{"system.snapshot", "system.services", "system.ports", "diagnostics.full", "engines.status", "tunnels.list", "subscriptions.status"} {
		step := registry.Execute(context.Background(), ToolCall{Name: name})
		if step.Status != "passed" || len(step.Evidence) != 1 || !strings.HasPrefix(step.Evidence[0], "{") && !strings.HasPrefix(step.Evidence[0], "[") {
			t.Fatalf("%s result = %+v", name, step)
		}
	}
	step := registry.Execute(context.Background(), ToolCall{Name: "connections.search", Arguments: map[string]string{"search": "192.168.90.4", "limit": "500"}})
	if step.Status != "passed" || search != "192.168.90.4" || limit != 50 {
		t.Fatalf("connections result=%+v search=%q limit=%d", step, search, limit)
	}
	if step := registry.Execute(context.Background(), ToolCall{Name: "system.packages", Arguments: map[string]string{"kind": "search", "query": "curl"}}); step.Status != "passed" {
		t.Fatalf("system.packages result = %+v", step)
	}
	for _, call := range []ToolCall{
		{Name: "system.files.list", Arguments: map[string]string{"path": "/opt/etc"}},
		{Name: "system.file.read_safe", Arguments: map[string]string{"path": "/opt/etc/demo.conf"}},
	} {
		if step := registry.Execute(context.Background(), call); step.Status != "passed" {
			t.Fatalf("%s result = %+v", call.Name, step)
		}
	}
	for _, call := range []ToolCall{
		{Name: "tunnels.status", Arguments: map[string]string{"id": "awg1"}},
		{Name: "routing.explain_client", Arguments: map[string]string{"client": "phone"}},
		{Name: "logs.tail", Arguments: map[string]string{"bucket": "app", "group": "routing", "level": "warn", "limit": "15"}},
		{Name: "proxy.groups", Arguments: map[string]string{"engine": "auto"}},
		{Name: "routing.explain_rule", Arguments: map[string]string{"destination": "youtube.com", "port": "443", "protocol": "tcp"}},
		{Name: "outbound.test", Arguments: map[string]string{"engine": "mihomo", "name": "ABV"}},
		{Name: "dns.explain", Arguments: map[string]string{"domain": "youtube.com", "sourceIp": "192.168.90.4", "queryType": "A"}},
		{Name: "connections.explain", Arguments: map[string]string{"source": "192.168.90.4", "sourcePort": "49152", "destination": "142.250.74.14", "destinationPort": "443", "protocol": "tcp"}},
		{Name: "remediation.propose", Arguments: map[string]string{"action": "tunnel.restart", "target": "awg1"}},
	} {
		if step := registry.Execute(context.Background(), call); step.Status != "passed" {
			t.Fatalf("%s result = %+v", call.Name, step)
		}
	}
}

func TestToolRegistryCatalogContainsOnlyReadOnlyTools(t *testing.T) {
	definitions := NewToolRegistry().Tools()
	wanted := map[string]bool{
		"system.snapshot": false, "system.services": false, "system.ports": false, "system.packages": false,
		"system.files.list": false, "system.file.read_safe": false,
		"diagnostics.full": false,
		"engines.status":   false, "tunnels.list": false, "tunnels.status": false,
		"subscriptions.status": false, "connections.search": false,
		"routing.explain_client": false, "logs.tail": false,
		"proxy.groups": false, "routing.explain_rule": false, "outbound.test": false,
		"dns.explain": false, "connections.explain": false,
		"system.diagnose_command": false,
		"remediation.propose":     false,
		"memory.learn_fact":       false, "memory.save_playbook": false, "memory.list_facts": false,
	}
	for _, definition := range definitions {
		if !definition.ReadOnly || definition.Risk != "none" {
			t.Fatalf("unsafe tool published: %+v", definition)
		}
		if _, ok := wanted[definition.Name]; ok {
			wanted[definition.Name] = true
		}
	}
	for name, found := range wanted {
		if !found {
			t.Fatalf("tool %s is missing", name)
		}
	}
}

func TestToolRegistry_DiagnoseCommand(t *testing.T) {
	registry := &ToolRegistry{
		run: func(_ context.Context, name string, args ...string) (*sysexec.Result, error) {
			if name != "/bin/sh" || len(args) != 2 || args[0] != "-c" {
				t.Fatalf("unexpected invocation: %s %v", name, args)
			}
			cmd := args[1]
			if strings.Contains(cmd, "fail") {
				return &sysexec.Result{Stderr: "command error", ExitCode: 1}, nil
			}
			return &sysexec.Result{Stdout: "ip route default via 192.168.1.1\n", ExitCode: 0}, nil
		},
	}

	// Safe command
	step := registry.Execute(context.Background(), ToolCall{
		Name:      "system.diagnose_command",
		Arguments: map[string]string{"command": "ip route show"},
	})
	if step.Status != "passed" {
		t.Fatalf("expected passed, got: %+v", step)
	}
	if len(step.Evidence) == 0 || !strings.Contains(step.Evidence[0], "192.168.1.1") {
		t.Fatalf("unexpected evidence: %+v", step.Evidence)
	}

	// Blocked unsafe command
	blockedStep := registry.Execute(context.Background(), ToolCall{
		Name:      "system.diagnose_command",
		Arguments: map[string]string{"command": "rm -rf /tmp"},
	})
	if blockedStep.Status != "error" {
		t.Fatalf("expected error for blocked command, got: %+v", blockedStep)
	}
	if len(blockedStep.Evidence) == 0 || !strings.Contains(blockedStep.Evidence[0], "запрещённую") {
		t.Fatalf("unexpected blocked evidence: %+v", blockedStep.Evidence)
	}
}

func TestToolRegistry_MemoryExecution(t *testing.T) {
	memStore, err := NewMemoryStore(t.TempDir() + "/test-mem.json")
	if err != nil {
		t.Fatalf("create test memory store: %v", err)
	}
	registry := NewToolRegistry()
	registry.SetMemoryStore(memStore)

	// Learn fact
	learnStep := registry.Execute(context.Background(), ToolCall{
		Name: "memory.learn_fact",
		Arguments: map[string]string{
			"category": "routing",
			"key":      "hero_4g",
			"fact":     "Repeater Hero 4G is at 192.168.90.2",
		},
	})
	if learnStep.Status != "passed" {
		t.Fatalf("expected passed for learn_fact, got: %+v", learnStep)
	}

	// Save playbook
	pbStep := registry.Execute(context.Background(), ToolCall{
		Name: "memory.save_playbook",
		Arguments: map[string]string{
			"symptom":           "tunnel_down: opkgtun10",
			"diagnosis":         "handshake failure",
			"remediationAction": "tunnel.restart",
			"remediationTarget": "opkgtun10",
			"explanation":       "Restarts tunnel interface to reconnect",
		},
	})
	if pbStep.Status != "passed" {
		t.Fatalf("expected passed for save_playbook, got: %+v", pbStep)
	}

	// List facts
	listStep := registry.Execute(context.Background(), ToolCall{
		Name: "memory.list_facts",
	})
	if listStep.Status != "passed" {
		t.Fatalf("expected passed for list_facts, got: %+v", listStep)
	}
	if len(listStep.Evidence) == 0 || !strings.Contains(listStep.Evidence[0], "Hero 4G") {
		t.Fatalf("expected Hero 4G in evidence, got: %+v", listStep.Evidence)
	}
}
