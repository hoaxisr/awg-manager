package router

import "testing"

// Слот tproxy узкий: инбаунды перехвата + системные route-правила. Ни
// outbound'ов, ни наборов, ни скаляра route.final — иначе перебьёт
// пользовательский выбор из общего слота (скаляры сливаются first-file-wins,
// режимный слот первый).
//
// Инбаундов ДВА, а не один (расхождение с брифом задачи, где стоит 1): режим
// tproxy принципиально работает парой — tproxy-in ловит UDP, redirect-in ловит
// TCP (TPROXY на established TCP не срабатывает на ядре 4.9-ndm-5, см.
// ensureTProxyInbound). Один инбаунд означал бы потерю всего TCP.
func TestBuildTProxySlotShape(t *testing.T) {
	cfg := buildTProxySlot(TProxyParams{SnifferEnabled: true})

	if len(cfg.Inbounds) != 2 {
		t.Fatalf("ожидалась пара инбаундов перехвата, получено %d", len(cfg.Inbounds))
	}
	tags := map[string]bool{}
	for _, in := range cfg.Inbounds {
		tags[in.Tag] = true
	}
	if !tags["tproxy-in"] || !tags["redirect-in"] {
		t.Errorf("ожидались инбаунды tproxy-in и redirect-in, получено %v", tags)
	}
	if !hasHijackDNSRule(cfg) {
		t.Error("системное правило hijack-dns обязано быть в режимном слоте")
	}
	if len(cfg.Outbounds) != 0 {
		t.Errorf("слот tproxy не должен писать outbounds, получено %d", len(cfg.Outbounds))
	}
	if len(cfg.Route.RuleSet) != 0 {
		t.Errorf("слот tproxy не должен писать rule_set, получено %d", len(cfg.Route.RuleSet))
	}
	if cfg.Route.Final != "" {
		t.Errorf("слот tproxy не должен писать route.final, получено %q", cfg.Route.Final)
	}
	if len(cfg.DNS.Servers) != 0 || len(cfg.DNS.Rules) != 0 || cfg.DNS.Final != "" {
		t.Errorf("слот tproxy не должен писать DNS, получено %+v", cfg.DNS)
	}
}

// Сниффер выключен — правила sniff в слоте быть не должно, hijack и обход
// приватных адресов остаются.
func TestBuildTProxySlotSnifferDisabled(t *testing.T) {
	cfg := buildTProxySlot(TProxyParams{SnifferEnabled: false})
	for i, r := range cfg.Route.Rules {
		if isSystemSniffRule(r) {
			t.Fatalf("правило sniff при выключенном сниффере (index %d)", i)
		}
	}
	if !isSystemHijackRule(cfg.Route.Rules[0]) {
		t.Errorf("первым правилом обязан быть hijack-dns, получено %+v", cfg.Route.Rules[0])
	}
	if cfg.Route.Rules[1].IPIsPrivate == nil {
		t.Errorf("вторым правилом обязан быть обход приватных адресов, получено %+v", cfg.Route.Rules[1])
	}
}

// Классы QoS — тоже захват трафика: их инбаунды пишет режимный слот, иначе
// после разделения они остались бы в общем и продублировались бы с ним.
func TestBuildTProxySlotQoSInbounds(t *testing.T) {
	cfg := buildTProxySlot(TProxyParams{
		SnifferEnabled: true,
		QoSClasses:     []qosClass{{DSCP: 46, Outbound: "vpn", TProxyPort: 51281, RedirectPort: 51291}},
	})
	got := map[string]int{}
	for _, in := range cfg.Inbounds {
		got[in.Tag] = in.ListenPort
	}
	if got["tproxy-qos-46"] != 51281 || got["redirect-qos-46"] != 51291 {
		t.Errorf("инбаунды класса QoS не попали в режимный слот: %v", got)
	}
}

// Режимный слот policy-tun устроен так же: tun-инбаунд вместо пары tproxy,
// системные правила те же, общего содержимого нет.
func TestBuildPolicyTunSlotShape(t *testing.T) {
	cfg := buildPolicyTunSlot(PolicyTunInboundSpec{
		Iface:    "opkgtun0",
		TunAddr4: "172.18.0.1/30",
		MTU:      1500,
	}, true, nil)

	if len(cfg.Inbounds) != 1 || cfg.Inbounds[0].Tag != "tun-in" {
		t.Fatalf("ожидался один tun-инбаунд, получено %+v", cfg.Inbounds)
	}
	if !hasHijackDNSRule(cfg) {
		t.Error("системное правило hijack-dns обязано быть в режимном слоте")
	}
	if len(cfg.Outbounds) != 0 || len(cfg.Route.RuleSet) != 0 || cfg.Route.Final != "" {
		t.Errorf("слот policy-tun не должен писать общее содержимое: %+v", cfg.Route)
	}
}
