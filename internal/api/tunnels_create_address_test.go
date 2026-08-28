package api

import "testing"

func createBodyWithAddress(addr string) string {
	return `{"name":"t","type":"awg","backend":"kernel",
		"interface":{"privateKey":"CA9lE1yLCcziI8Oq0dXDYr3QtdzFCEsKYw8sxAQ132o=","address":"` + addr + `","mtu":1280},
		"peer":{"publicKey":"hOPHc7ZBk0dGrLLDFrCG7WHYzZ8SS5xBWMzOJ9CkNFo=","endpoint":"192.0.2.1:51820","allowedIPs":["0.0.0.0/0"]}}`
}

// tunnel.Config несёт адрес БЕЗ маски и отдельным полем IPv6 — так его строит
// оркестраторный путь (StoredToConfig → SplitAddresses). Хендлер клал туда
// сырую строку формы: при двух адресах через запятую вся строка уезжала в NDMS
// как адрес IPv4, а IPv6 не настраивался вовсе.
func TestCreate_SplitsDualStackAddress(t *testing.T) {
	stub := &stubTunnelSvc{}
	h, _ := newTunnelsUpdateHarness(t, stub)

	postCreate(t, h, createBodyWithAddress("10.8.0.2/24, fd00::2/64"))

	if stub.createdCfg == nil {
		t.Fatal("Create не был вызван")
	}
	if stub.createdCfg.Address != "10.8.0.2" {
		t.Errorf("IPv4 = %q, want 10.8.0.2 (без маски и без хвоста IPv6)", stub.createdCfg.Address)
	}
	if stub.createdCfg.AddressIPv6 != "fd00::2" {
		t.Errorf("IPv6 = %q, want fd00::2", stub.createdCfg.AddressIPv6)
	}
	if stub.createdCfg.AddressPrefix != 24 {
		t.Errorf("AddressPrefix = %d, want 24 — маска пользователя обязана доехать", stub.createdCfg.AddressPrefix)
	}
}

func TestCreate_SingleAddressLosesMask(t *testing.T) {
	stub := &stubTunnelSvc{}
	h, _ := newTunnelsUpdateHarness(t, stub)

	postCreate(t, h, createBodyWithAddress("10.8.0.2/24"))

	if stub.createdCfg == nil {
		t.Fatal("Create не был вызван")
	}
	if stub.createdCfg.Address != "10.8.0.2" {
		t.Errorf("Address = %q, want 10.8.0.2 — маску ставит оператор", stub.createdCfg.Address)
	}
	if stub.createdCfg.AddressIPv6 != "" {
		t.Errorf("IPv6 не задавался, got %q", stub.createdCfg.AddressIPv6)
	}
	if stub.createdCfg.AddressPrefix != 24 {
		t.Errorf("AddressPrefix = %d, want 24", stub.createdCfg.AddressPrefix)
	}
}
