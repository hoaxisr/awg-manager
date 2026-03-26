package staticroute

import "testing"

func TestParseCIDR(t *testing.T) {
	tests := []struct {
		cidr    string
		network string
		mask    string
		wantErr bool
	}{
		{"10.0.0.0/8", "10.0.0.0", "255.0.0.0", false},
		{"192.168.1.0/24", "192.168.1.0", "255.255.255.0", false},
		{"172.16.0.0/12", "172.16.0.0", "255.240.0.0", false},
		{"1.2.3.4/32", "1.2.3.4", "", false},
		{"0.0.0.0/0", "0.0.0.0", "0.0.0.0", false},
		{"invalid", "", "", true},
		{"fd00::/64", "", "", true},
	}
	for _, tt := range tests {
		network, mask, err := parseCIDR(tt.cidr)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseCIDR(%q) error = %v, wantErr %v", tt.cidr, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && (network != tt.network || mask != tt.mask) {
			t.Errorf("parseCIDR(%q) = (%q, %q), want (%q, %q)", tt.cidr, network, mask, tt.network, tt.mask)
		}
	}
}

func TestResolveNDMSName_SystemTunnel(t *testing.T) {
	s := &ServiceImpl{}
	name, err := s.resolveNDMSName("system:Wireguard0")
	if err != nil {
		t.Fatal(err)
	}
	if name != "Wireguard0" {
		t.Errorf("got %q, want Wireguard0", name)
	}
}

func TestResolveNDMSName_KernelTunnel(t *testing.T) {
	s := &ServiceImpl{}
	name, err := s.resolveNDMSName("awg10")
	if err != nil {
		t.Fatal(err)
	}
	if name != "OpkgTun10" {
		t.Errorf("got %q, want OpkgTun10", name)
	}
}

func TestResolveNDMSName_WANNoModel(t *testing.T) {
	s := &ServiceImpl{}
	_, err := s.resolveNDMSName("wan:ppp0")
	if err == nil {
		t.Error("expected error for WAN without model")
	}
}

type mockWANModel struct {
	ids map[string]string
}

func (m *mockWANModel) IDFor(kernelName string) string {
	return m.ids[kernelName]
}

func TestResolveNDMSName_WAN(t *testing.T) {
	s := &ServiceImpl{
		wanModel: &mockWANModel{ids: map[string]string{"ppp0": "PPPoE0"}},
	}
	name, err := s.resolveNDMSName("wan:ppp0")
	if err != nil {
		t.Fatal(err)
	}
	if name != "PPPoE0" {
		t.Errorf("got %q, want PPPoE0", name)
	}
}
