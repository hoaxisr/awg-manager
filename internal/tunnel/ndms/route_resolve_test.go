package ndms

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// newTestClientWithMux creates a ClientImpl that routes RCI requests to a local
// test server. The handler receives paths like "/rci/show/interface/ISP".
func newTestClientWithMux(handler http.Handler) (*ClientImpl, *httptest.Server) {
	srv := httptest.NewServer(handler)
	// Override transport to redirect localhost:79 → test server.
	client := &ClientImpl{
		timeout: defaultTimeout,
		httpClient: &http.Client{
			Transport: &rewriteTransport{
				targetURL: srv.URL,
				base:      http.DefaultTransport,
			},
		},
	}
	return client, srv
}

// rewriteTransport rewrites all requests targeting rciBaseURL to the test server.
type rewriteTransport struct {
	targetURL string
	base      http.RoundTripper
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Replace http://localhost:79 → test server, preserving path + query.
	newURL, _ := url.Parse(t.targetURL + req.URL.Path)
	newURL.RawQuery = req.URL.RawQuery
	req.URL = newURL
	return t.base.RoundTrip(req)
}

// rciMux creates an http.ServeMux with predefined RCI responses.
// responses maps path suffix (e.g., "/show/interface/ISP") to JSON response body.
// systemNames maps NDMS name → kernel name for /show/interface/system-name?name=X.
func rciMux(responses map[string]any, systemNames map[string]string) *http.ServeMux {
	mux := http.NewServeMux()

	// system-name handler (query-param based)
	if len(systemNames) > 0 {
		mux.HandleFunc("/rci/show/interface/system-name", func(w http.ResponseWriter, r *http.Request) {
			name := r.URL.Query().Get("name")
			if sysName, ok := systemNames[name]; ok {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(sysName)
				return
			}
			http.NotFound(w, r)
		})
	}

	for path, resp := range responses {
		data, _ := json.Marshal(resp)
		mux.HandleFunc("/rci"+path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write(data)
		})
	}
	return mux
}

// ========================================================================
// getSystemName — NDMS logical name → kernel name resolution
// ========================================================================

func TestGetSystemName_ISP_to_eth3(t *testing.T) {
	c, srv := newTestClientWithMux(rciMux(nil, map[string]string{
		"ISP": "eth3",
	}))
	defer srv.Close()

	got := c.getSystemName(context.Background(), "ISP")
	if got != "eth3" {
		t.Errorf("getSystemName(ISP) = %q, want %q", got, "eth3")
	}
}

func TestGetSystemName_PPPoE_to_ppp0(t *testing.T) {
	c, srv := newTestClientWithMux(rciMux(nil, map[string]string{
		"PPPoE1": "ppp0",
	}))
	defer srv.Close()

	got := c.getSystemName(context.Background(), "PPPoE1")
	if got != "ppp0" {
		t.Errorf("getSystemName(PPPoE1) = %q, want %q", got, "ppp0")
	}
}

func TestGetSystemName_UnknownInterface(t *testing.T) {
	// No system-name mapping → RCI returns 404 → fallback to original name.
	c, srv := newTestClientWithMux(rciMux(nil, nil))
	defer srv.Close()

	got := c.getSystemName(context.Background(), "NoSuchIface")
	if got != "NoSuchIface" {
		t.Errorf("getSystemName(NoSuchIface) = %q, want %q", got, "NoSuchIface")
	}
}

// ========================================================================
// GetGatewayForInterface — end-to-end with system-name resolution
// ========================================================================

// Scenario: User 2 — IPoE ISP with DHCP, route table uses eth3.
// getSystemName("ISP") → "eth3", then find gateway for eth3 in routes.
func TestGetGatewayForInterface_IPoE_ResolvedName(t *testing.T) {
	c, srv := newTestClientWithMux(rciMux(map[string]any{
		"/show/ip/route": []rciRouteEntry{
			{Destination: "0.0.0.0/0", Gateway: "203.0.113.1", Interface: "eth3"},
			{Destination: "203.0.113.0/25", Gateway: "", Interface: "eth3"},
			{Destination: "172.16.10.0/24", Gateway: "", Interface: "br0"},
		},
	}, map[string]string{
		"ISP": "eth3",
	}))
	defer srv.Close()

	gw, err := c.GetGatewayForInterface(context.Background(), "ISP")
	if err != nil {
		t.Fatalf("GetGatewayForInterface(ISP) error: %v", err)
	}
	if gw != "203.0.113.1" {
		t.Errorf("GetGatewayForInterface(ISP) = %q, want %q", gw, "203.0.113.1")
	}
}

// Scenario: IPoE with DHCP where gateway is 0.0.0.0 (rare but valid).
// Should compute gateway from interface IP + mask.
func TestGetGatewayForInterface_DHCP_ZeroGateway(t *testing.T) {
	c, srv := newTestClientWithMux(rciMux(map[string]any{
		"/show/ip/route": []rciRouteEntry{
			{Destination: "0.0.0.0/0", Gateway: "0.0.0.0", Interface: "eth3"},
			{Destination: "192.168.20.0/24", Gateway: "", Interface: "eth3"},
		},
		"/show/interface/ISP": rciInterfaceInfo{
			Address: "192.168.20.55",
			Mask:    "255.255.255.0",
		},
		"/show/ip/dhcp/client": []rciDHCPClient{
			{ID: "ISP", Name: "ISP", State: "bound"},
		},
	}, map[string]string{
		"ISP": "eth3",
	}))
	defer srv.Close()

	gw, err := c.GetGatewayForInterface(context.Background(), "ISP")
	if err != nil {
		t.Fatalf("GetGatewayForInterface(ISP) error: %v", err)
	}
	// computeSubnetFirstIP(192.168.20.55, 255.255.255.0) = 192.168.20.1
	if gw != "192.168.20.1" {
		t.Errorf("GetGatewayForInterface(ISP) = %q, want %q", gw, "192.168.20.1")
	}
}

// Scenario: PPPoE — kernel name ppp0, route has ppp0 with 0.0.0.0 gateway.
// PPPoE is not DHCP → returns NDMS name as gateway placeholder.
func TestGetGatewayForInterface_PPPoE_ViaRouteTable(t *testing.T) {
	c, srv := newTestClientWithMux(rciMux(map[string]any{
		"/show/ip/route": []rciRouteEntry{
			{Destination: "0.0.0.0/0", Gateway: "0.0.0.0", Interface: "ppp0"},
			{Destination: "10.20.30.1/32", Gateway: "", Interface: "ppp0"},
		},
		"/show/interface/PPPoE1": rciInterfaceInfo{
			Address: "100.64.1.5",
			Mask:    "255.255.255.255",
		},
	}, map[string]string{
		"PPPoE1": "ppp0",
	}))
	defer srv.Close()

	gw, err := c.GetGatewayForInterface(context.Background(), "PPPoE1")
	if err != nil {
		t.Fatalf("GetGatewayForInterface(PPPoE1) error: %v", err)
	}
	// PPPoE: gateway 0.0.0.0, not DHCP → returns NDMS name
	if gw != "PPPoE1" {
		t.Errorf("GetGatewayForInterface(PPPoE1) = %q, want %q", gw, "PPPoE1")
	}
}

// Scenario: PPPoE where ppp0 has no route entry at all.
// Falls back to NDMS interface existence check.
func TestGetGatewayForInterface_PPPoE_NoRouteEntry(t *testing.T) {
	c, srv := newTestClientWithMux(rciMux(map[string]any{
		"/show/ip/route": []rciRouteEntry{
			{Destination: "172.16.10.0/24", Gateway: "", Interface: "br0"},
		},
		"/show/interface/PPPoE1": rciInterfaceInfo{
			Address: "100.64.1.5",
			Mask:    "255.255.255.255",
		},
	}, map[string]string{
		"PPPoE1": "ppp0",
	}))
	defer srv.Close()

	gw, err := c.GetGatewayForInterface(context.Background(), "PPPoE1")
	if err != nil {
		t.Fatalf("GetGatewayForInterface(PPPoE1) error: %v", err)
	}
	if gw != "PPPoE1" {
		t.Errorf("GetGatewayForInterface(PPPoE1) = %q, want %q", gw, "PPPoE1")
	}
}

// Scenario: Route table already uses NDMS names — direct match.
func TestGetGatewayForInterface_DirectMatch(t *testing.T) {
	c, srv := newTestClientWithMux(rciMux(map[string]any{
		"/show/ip/route": []rciRouteEntry{
			{Destination: "0.0.0.0/0", Gateway: "10.0.0.1", Interface: "ISP"},
			{Destination: "10.0.0.0/24", Gateway: "", Interface: "ISP"},
		},
	}, map[string]string{
		"ISP": "ISP", // system-name == NDMS name on some firmware
	}))
	defer srv.Close()

	gw, err := c.GetGatewayForInterface(context.Background(), "ISP")
	if err != nil {
		t.Fatalf("GetGatewayForInterface(ISP) error: %v", err)
	}
	if gw != "10.0.0.1" {
		t.Errorf("GetGatewayForInterface(ISP) = %q, want %q", gw, "10.0.0.1")
	}
}

// Scenario: Interface doesn't exist in NDMS → error.
func TestGetGatewayForInterface_UnknownInterface(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rci/show/ip/route", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]rciRouteEntry{
			{Destination: "0.0.0.0/0", Gateway: "10.0.0.1", Interface: "eth0"},
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	c, srv := newTestClientWithMux(mux)
	defer srv.Close()

	_, err := c.GetGatewayForInterface(context.Background(), "NoSuchIface")
	if err == nil {
		t.Error("GetGatewayForInterface(NoSuchIface) expected error, got nil")
	}
}

// ========================================================================
// GetIPv6GatewayForInterface
// ========================================================================

// Scenario: IPv6 gateway resolved via system-name.
func TestGetIPv6GatewayForInterface_ResolvedName(t *testing.T) {
	c, srv := newTestClientWithMux(rciMux(map[string]any{
		"/show/ipv6/route": []rciIPv6RouteEntry{
			{Destination: "::/0", Gateway: "fe80::1", Interface: "eth3"},
		},
	}, map[string]string{
		"ISP": "eth3",
	}))
	defer srv.Close()

	gw, err := c.GetIPv6GatewayForInterface(context.Background(), "ISP")
	if err != nil {
		t.Fatalf("GetIPv6GatewayForInterface(ISP) error: %v", err)
	}
	if gw != "fe80::1" {
		t.Errorf("GetIPv6GatewayForInterface(ISP) = %q, want %q", gw, "fe80::1")
	}
}

// Scenario: PPPoE IPv6 — gateway "::" → return NDMS name.
func TestGetIPv6GatewayForInterface_PPPoE_Fallback(t *testing.T) {
	c, srv := newTestClientWithMux(rciMux(map[string]any{
		"/show/ipv6/route": []rciIPv6RouteEntry{
			{Destination: "::/0", Gateway: "::", Interface: "ppp0"},
		},
		"/show/interface/PPPoE1": rciInterfaceInfo{
			Address: "100.64.1.5",
			Mask:    "255.255.255.255",
		},
	}, map[string]string{
		"PPPoE1": "ppp0",
	}))
	defer srv.Close()

	gw, err := c.GetIPv6GatewayForInterface(context.Background(), "PPPoE1")
	if err != nil {
		t.Fatalf("GetIPv6GatewayForInterface(PPPoE1) error: %v", err)
	}
	if gw != "PPPoE1" {
		t.Errorf("GetIPv6GatewayForInterface(PPPoE1) = %q, want %q", gw, "PPPoE1")
	}
}

// ========================================================================
// Full scenario: User 2 diagnostics (arm64, IPoE ISP with public IP)
// ========================================================================

func TestFullScenario_User2_ISP_PublicIP(t *testing.T) {
	c, srv := newTestClientWithMux(rciMux(map[string]any{
		"/show/ip/route": []rciRouteEntry{
			{Destination: "0.0.0.0/0", Gateway: "203.0.113.1", Interface: "eth3"},
			{Destination: "203.0.113.0/25", Gateway: "", Interface: "eth3"},
			{Destination: "172.16.50.0/24", Gateway: "", Interface: "eth2.9"},
			{Destination: "172.16.10.0/24", Gateway: "", Interface: "br0"},
			{Destination: "10.20.20.0/24", Gateway: "", Interface: "nwg1"},
		},
		"/show/ip/dhcp/client": []rciDHCPClient{
			{ID: "ISP", Name: "ISP", State: "bound"},
		},
	}, map[string]string{
		"ISP": "eth3",
	}))
	defer srv.Close()

	gw, err := c.GetGatewayForInterface(context.Background(), "ISP")
	if err != nil {
		t.Fatalf("User 2 scenario: GetGatewayForInterface(ISP) error: %v", err)
	}
	if gw != "203.0.113.1" {
		t.Errorf("User 2 scenario: gateway = %q, want %q", gw, "203.0.113.1")
	}
}

// ========================================================================
// Full scenario: User 1 (mipsle, PPPoE1 as primary WAN)
// ========================================================================

func TestFullScenario_User1_PPPoE(t *testing.T) {
	c, srv := newTestClientWithMux(rciMux(map[string]any{
		"/show/ip/route": []rciRouteEntry{
			{Destination: "0.0.0.0/0", Gateway: "0.0.0.0", Interface: "ppp0"},
			{Destination: "10.20.30.1/32", Gateway: "", Interface: "ppp0"},
			{Destination: "172.16.10.0/24", Gateway: "", Interface: "br0"},
		},
		"/show/interface/PPPoE1": rciInterfaceInfo{
			Address: "100.64.1.5",
			Mask:    "255.255.255.255",
			Type:    "PPPoE",
		},
	}, map[string]string{
		"PPPoE1": "ppp0",
	}))
	defer srv.Close()

	gw, err := c.GetGatewayForInterface(context.Background(), "PPPoE1")
	if err != nil {
		t.Fatalf("User 1 scenario: GetGatewayForInterface(PPPoE1) error: %v", err)
	}
	if gw != "PPPoE1" {
		t.Errorf("User 1 scenario: gateway = %q, want %q", gw, "PPPoE1")
	}
}

// ========================================================================
// GetDefaultGatewayInterface — tunnel filtering
// ========================================================================

func TestGetDefaultGatewayInterface_SkipsTunnels(t *testing.T) {
	c, srv := newTestClientWithMux(rciMux(map[string]any{
		"/show/ip/route": []rciRouteEntry{
			{Destination: "0.0.0.0/0", Gateway: "10.0.0.1", Interface: "awg0"},
			{Destination: "0.0.0.0/0", Gateway: "203.0.113.1", Interface: "eth3"},
		},
	}, nil))
	defer srv.Close()

	iface, err := c.GetDefaultGatewayInterface(context.Background())
	if err != nil {
		t.Fatalf("GetDefaultGatewayInterface() error: %v", err)
	}
	if iface != "eth3" {
		t.Errorf("GetDefaultGatewayInterface() = %q, want %q", iface, "eth3")
	}
}

func TestGetDefaultGatewayInterface_AllTunnelRoutes(t *testing.T) {
	c, srv := newTestClientWithMux(rciMux(map[string]any{
		"/show/ip/route": []rciRouteEntry{
			{Destination: "0.0.0.0/0", Gateway: "10.0.0.1", Interface: "awg0"},
			{Destination: "0.0.0.0/0", Gateway: "10.0.0.2", Interface: "nwg1"},
		},
	}, nil))
	defer srv.Close()

	_, err := c.GetDefaultGatewayInterface(context.Background())
	if err == nil {
		t.Error("GetDefaultGatewayInterface() expected error when all routes are tunnels, got nil")
	}
}

func TestGetDefaultGatewayInterface_Normal(t *testing.T) {
	c, srv := newTestClientWithMux(rciMux(map[string]any{
		"/show/ip/route": []rciRouteEntry{
			{Destination: "0.0.0.0/0", Gateway: "10.0.0.1", Interface: "ISP"},
		},
	}, nil))
	defer srv.Close()

	iface, err := c.GetDefaultGatewayInterface(context.Background())
	if err != nil {
		t.Fatalf("GetDefaultGatewayInterface() error: %v", err)
	}
	if iface != "ISP" {
		t.Errorf("GetDefaultGatewayInterface() = %q, want %q", iface, "ISP")
	}
}

// ========================================================================
// GetDefaultGateway — gateway IP + interface resolution
// ========================================================================

func TestGetDefaultGateway_PPPoE_ZeroGateway(t *testing.T) {
	c, srv := newTestClientWithMux(rciMux(map[string]any{
		"/show/ip/route": []rciRouteEntry{
			{Destination: "0.0.0.0/0", Gateway: "0.0.0.0", Interface: "ppp0"},
		},
	}, nil))
	defer srv.Close()

	gateway, iface, err := c.GetDefaultGateway(context.Background())
	if err != nil {
		t.Fatalf("GetDefaultGateway() error: %v", err)
	}
	if gateway != "ppp0" {
		t.Errorf("GetDefaultGateway() gateway = %q, want %q", gateway, "ppp0")
	}
	if iface != "ppp0" {
		t.Errorf("GetDefaultGateway() iface = %q, want %q", iface, "ppp0")
	}
}

func TestGetDefaultGateway_Normal(t *testing.T) {
	c, srv := newTestClientWithMux(rciMux(map[string]any{
		"/show/ip/route": []rciRouteEntry{
			{Destination: "0.0.0.0/0", Gateway: "10.0.0.1", Interface: "eth0"},
		},
	}, nil))
	defer srv.Close()

	gateway, iface, err := c.GetDefaultGateway(context.Background())
	if err != nil {
		t.Fatalf("GetDefaultGateway() error: %v", err)
	}
	if gateway != "10.0.0.1" {
		t.Errorf("GetDefaultGateway() gateway = %q, want %q", gateway, "10.0.0.1")
	}
	if iface != "eth0" {
		t.Errorf("GetDefaultGateway() iface = %q, want %q", iface, "eth0")
	}
}

func TestGetDefaultGateway_SkipsTunnels(t *testing.T) {
	c, srv := newTestClientWithMux(rciMux(map[string]any{
		"/show/ip/route": []rciRouteEntry{
			{Destination: "0.0.0.0/0", Gateway: "10.0.0.1", Interface: "nwg0"},
			{Destination: "0.0.0.0/0", Gateway: "203.0.113.1", Interface: "eth3"},
		},
	}, nil))
	defer srv.Close()

	gateway, iface, err := c.GetDefaultGateway(context.Background())
	if err != nil {
		t.Fatalf("GetDefaultGateway() error: %v", err)
	}
	if gateway != "203.0.113.1" {
		t.Errorf("GetDefaultGateway() gateway = %q, want %q", gateway, "203.0.113.1")
	}
	if iface != "eth3" {
		t.Errorf("GetDefaultGateway() iface = %q, want %q", iface, "eth3")
	}
}

// ========================================================================
// computeSubnetFirstIP — subnet gateway computation
// ========================================================================

func TestComputeSubnetFirstIP(t *testing.T) {
	tests := []struct {
		name    string
		address string
		mask    string
		want    string
		wantErr bool
	}{
		{name: "class C /24", address: "192.168.1.50", mask: "255.255.255.0", want: "192.168.1.1"},
		{name: "class B /16", address: "10.0.0.100", mask: "255.255.0.0", want: "10.0.0.1"},
		{name: "/25 subnet", address: "203.0.113.55", mask: "255.255.255.128", want: "203.0.113.1"},
		{name: "/30 subnet", address: "172.16.50.10", mask: "255.255.255.252", want: "172.16.50.9"},
		{name: "invalid address", address: "invalid", mask: "255.255.255.0", wantErr: true},
		{name: "invalid mask", address: "192.168.1.1", mask: "invalid", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := computeSubnetFirstIP(tt.address, tt.mask)
			if tt.wantErr {
				if err == nil {
					t.Errorf("computeSubnetFirstIP(%q, %q) expected error, got %q", tt.address, tt.mask, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("computeSubnetFirstIP(%q, %q) error: %v", tt.address, tt.mask, err)
			}
			if got != tt.want {
				t.Errorf("computeSubnetFirstIP(%q, %q) = %q, want %q", tt.address, tt.mask, got, tt.want)
			}
		})
	}
}

// ========================================================================
// IsDHCPClientBound — DHCP state detection
// ========================================================================

func TestIsDHCPClientBound_States(t *testing.T) {
	tests := []struct {
		name  string
		state string
		want  bool
	}{
		{name: "bound", state: "bound", want: true},
		{name: "renew", state: "renew", want: true},
		{name: "expired", state: "expired", want: false},
		{name: "init", state: "init", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, srv := newTestClientWithMux(rciMux(map[string]any{
				"/show/ip/dhcp/client": []rciDHCPClient{
					{ID: "ISP", Name: "ISP", State: tt.state},
				},
			}, nil))
			defer srv.Close()

			got := c.IsDHCPClientBound(context.Background(), "ISP")
			if got != tt.want {
				t.Errorf("IsDHCPClientBound(ISP) with state %q = %v, want %v", tt.state, got, tt.want)
			}
		})
	}

	t.Run("empty list", func(t *testing.T) {
		c, srv := newTestClientWithMux(rciMux(map[string]any{
			"/show/ip/dhcp/client": []rciDHCPClient{},
		}, nil))
		defer srv.Close()

		got := c.IsDHCPClientBound(context.Background(), "ISP")
		if got {
			t.Error("IsDHCPClientBound(ISP) with empty list = true, want false")
		}
	})
}

func TestIsDHCPClientBound_MatchByName(t *testing.T) {
	c, srv := newTestClientWithMux(rciMux(map[string]any{
		"/show/ip/dhcp/client": []rciDHCPClient{
			{ID: "eth3", Name: "ISP", State: "bound"},
		},
	}, nil))
	defer srv.Close()

	ctx := context.Background()

	if !c.IsDHCPClientBound(ctx, "ISP") {
		t.Error("IsDHCPClientBound(ISP) = false, want true (match by Name)")
	}
	if !c.IsDHCPClientBound(ctx, "eth3") {
		t.Error("IsDHCPClientBound(eth3) = false, want true (match by ID)")
	}
	if c.IsDHCPClientBound(ctx, "PPPoE1") {
		t.Error("IsDHCPClientBound(PPPoE1) = true, want false (no match)")
	}
}

func TestIsDHCPClientBound_RCIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rci/show/ip/dhcp/client", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	c, srv := newTestClientWithMux(mux)
	defer srv.Close()

	got := c.IsDHCPClientBound(context.Background(), "ISP")
	if got {
		t.Error("IsDHCPClientBound(ISP) with server error = true, want false")
	}
}
