package router

// TProxyParams — вход генератора режимного слота tproxy (20-tproxy.json).
// Всё, чем режим захвата отличается от других: включён ли сниффер, какой
// UDP-таймаут держат инбаунды и какие классы QoS активны.
type TProxyParams struct {
	SnifferEnabled bool
	UDPTimeout     string
	QoSClasses     []qosClass
}

// buildTProxySlot собирает режимный слот tproxy целиком: пара инбаундов
// перехвата (tproxy-in для UDP + redirect-in для TCP, см. ensureTProxyInbound),
// инбаунды классов QoS и системные route-правила (sniff / hijack-dns /
// ip_is_private / route-options udp_timeout).
//
// Чего здесь нет намеренно: outbound'ов, наборов правил, пользовательских
// правил и скаляра route.final — их пишет общий слот 21-routing.json. Режимный
// слот сливается ПЕРВЫМ, а sing-box берёт скаляры из первого файла, поэтому
// любой скаляр отсюда перебил бы пользовательский выбор.
func buildTProxySlot(p TProxyParams) *RouterConfig {
	cfg := NewEmptyConfig()
	// NewEmptyConfig ставит route.final="direct" — в режимном слоте его быть не
	// должно (см. выше). Пустая строка не маршалится (omitempty).
	cfg.Route.Final = ""
	cfg.Inbounds = ensureTProxyInbound(nil, p.UDPTimeout)
	cfg.Inbounds, _ = ensureQoSInbounds(cfg.Inbounds, p.QoSClasses, p.UDPTimeout)
	cfg.EnsureSystemRules(p.SnifferEnabled)
	cfg.EnsureUDPTimeoutRule(resolveUDPTimeout(p.UDPTimeout))
	return cfg
}
