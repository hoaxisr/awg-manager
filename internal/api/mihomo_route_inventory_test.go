package api

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/mihomonative"
)

type RouteKind string

const (
	KindReadOnly             RouteKind = "read_only"
	KindOrdinaryMutation     RouteKind = "ordinary_mutation"
	KindRecoveryReconcile    RouteKind = "recovery_reconcile"
	KindDiagnosticInspection RouteKind = "diagnostic_inspection"
	KindClashProxy           RouteKind = "clash_proxy"
)

// KnownMihomoRouteClassification contains the binding classification for all routes
// registered by MihomoHandler. If a new route is registered without being classified,
// TestMihomoRouteInventory_AllRoutesClassified will fail immediately.
var KnownMihomoRouteClassification = map[string]RouteKind{
	// Read-only endpoints
	"GET /api/mihomo/status":                 KindReadOnly,
	"GET /api/mihomo/config":                 KindReadOnly,
	"GET /api/mihomo/recovery/evidence":      KindReadOnly,
	"GET /api/mihomo/native/proxies":         KindReadOnly,
	"GET /api/mihomo/native/proxies/{id}":    KindReadOnly,
	"GET /api/mihomo/native/subscriptions":   KindReadOnly,
	"GET /api/mihomo/native/subscriptions/{id}": KindReadOnly,
	"GET /api/mihomo/native/groups":          KindReadOnly,
	"GET /api/mihomo/native/groups/{id}/references": KindReadOnly,
	"GET /api/mihomo/native/rules":           KindReadOnly,
	"GET /api/mihomo/native/rules/unsupported": KindReadOnly,
	"GET /api/router/mihomo/rules/unsupported": KindReadOnly,
	"GET /api/mihomo/native/rule-providers":  KindReadOnly,
	"GET /api/mihomo/router/inspect/stream":  KindReadOnly,

	// Ordinary mutations (must fail-closed with 503 RECOVERY_REQUIRED in degraded mode)
	"POST /api/mihomo/install":                            KindOrdinaryMutation,
	"POST /api/mihomo/update":                             KindOrdinaryMutation,
	"POST /api/mihomo/uninstall":                          KindOrdinaryMutation,
	"POST /api/mihomo/reload":                             KindOrdinaryMutation,
	"POST /api/mihomo/native/proxies":                     KindOrdinaryMutation,
	"PUT /api/mihomo/native/proxies/{id}":                 KindOrdinaryMutation,
	"DELETE /api/mihomo/native/proxies/{id}":              KindOrdinaryMutation,
	"POST /api/mihomo/native/subscriptions":              KindOrdinaryMutation,
	"PUT /api/mihomo/native/subscriptions/{id}":          KindOrdinaryMutation,
	"DELETE /api/mihomo/native/subscriptions/{id}":       KindOrdinaryMutation,
	"POST /api/mihomo/native/subscriptions/{id}/refresh": KindOrdinaryMutation,
	"POST /api/mihomo/native/groups":                      KindOrdinaryMutation,
	"PUT /api/mihomo/native/groups/{id}":                  KindOrdinaryMutation,
	"DELETE /api/mihomo/native/groups/{id}":               KindOrdinaryMutation,
	"POST /api/mihomo/native/rules":                       KindOrdinaryMutation,
	"PUT /api/mihomo/native/rules/{id}":                   KindOrdinaryMutation,
	"DELETE /api/mihomo/native/rules/{id}":                KindOrdinaryMutation,
	"PUT /api/mihomo/native/rules/order":                  KindOrdinaryMutation,
	"POST /api/mihomo/native/rules/unsupported/delete":    KindOrdinaryMutation,
	"POST /api/router/mihomo/rules/unsupported/delete":    KindOrdinaryMutation,
	"POST /api/mihomo/native/rule-providers":              KindOrdinaryMutation,
	"PUT /api/mihomo/native/rule-providers/{id}":          KindOrdinaryMutation,
	"DELETE /api/mihomo/native/rule-providers/{id}":       KindOrdinaryMutation,
	"POST /api/mihomo/native/reset":                       KindOrdinaryMutation,

	// Explicit recovery exception
	"POST /api/mihomo/recovery/reconcile": KindRecoveryReconcile,

	// Read-only diagnostic inspection POST
	"POST /api/mihomo/router/inspect": KindDiagnosticInspection,

	// Clash proxy endpoints
	"/api/mihomo/clash":  KindClashProxy,
	"/api/mihomo/clash/": KindClashProxy,
}

type patternCollector struct {
	patterns []string
}

func (c *patternCollector) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	c.patterns = append(c.patterns, pattern)
}

func TestMihomoRouteInventory_AllRoutesClassified(t *testing.T) {
	native, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}

	h := NewMihomoHandler(&fakeMihomoEngine{configDir: t.TempDir()})
	h.SetNativeStore(native)

	collector := &patternCollector{}
	h.RegisterRoutesTo(collector, nil)

	registeredSet := make(map[string]bool)
	for _, pattern := range collector.patterns {
		registeredSet[pattern] = true
		kind, ok := KnownMihomoRouteClassification[pattern]
		if !ok {
			t.Fatalf("UNCLASSIFIED ROUTE REGISTERED: %q must be classified in KnownMihomoRouteClassification", pattern)
		}

		if strings.HasPrefix(pattern, "GET ") {
			if kind != KindReadOnly {
				t.Fatalf("GET route %q classified as %v, expected KindReadOnly", pattern, kind)
			}
		} else if strings.HasPrefix(pattern, "POST ") || strings.HasPrefix(pattern, "PUT ") || strings.HasPrefix(pattern, "DELETE ") {
			if kind != KindOrdinaryMutation && kind != KindRecoveryReconcile && kind != KindDiagnosticInspection {
				t.Fatalf("non-GET route %q classified as %v, expected ordinary mutation, recovery reconcile, or diagnostic inspection", pattern, kind)
			}
		}
	}

	// Verify no stale classifications in table
	for pattern := range KnownMihomoRouteClassification {
		if !registeredSet[pattern] {
			t.Errorf("KnownMihomoRouteClassification contains route %q that is not registered by MihomoHandler", pattern)
		}
	}
}
