package router

import "testing"

func TestEnsurePolicyTunInbound(t *testing.T) {
	in := []Inbound{
		{Type: "tproxy", Tag: "tproxy-in", ListenPort: 51271},
		{Type: "redirect", Tag: "redirect-in", ListenPort: 51272},
		{Type: "tproxy", Tag: "tproxy-qos-34", ListenPort: 51281},
	}
	out := ensurePolicyTunInbound(in, PolicyTunInboundSpec{
		Iface: "opkgtun0", TunAddr4: "172.18.0.1/30", TunAddr6: "fdfe:dcba:9876::1/126",
		MTU: 1500, Stack: "gvisor",
	})
	var tun *Inbound
	for i := range out {
		if out[i].Tag == "tproxy-in" || out[i].Tag == "redirect-in" {
			t.Fatalf("tproxy inbound survived: %s", out[i].Tag)
		}
		if out[i].Tag == "tun-in" {
			tun = &out[i]
		}
	}
	if tun == nil {
		t.Fatal("no tun-in")
	}
	if tun.InterfaceName != "opkgtun0" || tun.MTU != 1500 || tun.Stack != "gvisor" {
		t.Fatalf("tun fields: %+v", tun)
	}
	if tun.AutoRoute == nil || *tun.AutoRoute || tun.AutoRedirect == nil || *tun.AutoRedirect ||
		tun.StrictRoute == nil || *tun.StrictRoute {
		t.Fatalf("auto_* must be explicit false: %+v", tun)
	}
	if len(tun.Address) != 2 {
		t.Fatalf("address: %v", tun.Address)
	}
	// QoS-инбаунды не тронуты
	found := false
	for _, i := range out {
		if i.Tag == "tproxy-qos-34" {
			found = true
		}
	}
	if !found {
		t.Fatal("qos inbound dropped")
	}
	// идемпотентность
	again := ensurePolicyTunInbound(out, PolicyTunInboundSpec{Iface: "opkgtun0", TunAddr4: "172.18.0.1/30", MTU: 1500, Stack: "gvisor"})
	n := 0
	for _, i := range again {
		if i.Tag == "tun-in" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("tun-in duplicated: %d", n)
	}
}

func TestFilterPolicyTunInbound(t *testing.T) {
	out := filterPolicyTunInbound([]Inbound{{Tag: "tun-in"}, {Tag: "x"}})
	if len(out) != 1 || out[0].Tag != "x" {
		t.Fatalf("%+v", out)
	}
}
