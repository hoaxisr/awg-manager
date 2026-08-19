package router

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Единственные места, где конструируется желаемый RestoreInputSpec.
//
// Их было четыре — по паре на режим: одна в Enable, одна в reconcile. Пары
// обязаны давать побайтово один спек на одних входах, иначе установившийся
// режим либо переустанавливает правила каждым тиком, либо (хуже) молча теряет
// часть правил при первой же переустановке. Ничто этого не стерегло: забытое
// в reconcile поле — например LANBridges, то есть весь DNS-RESCUE — не роняло
// ни одного теста. Один билдер на режим делает такой дрейф невыразимым.

// buildTproxySpec — режим tproxy: полный перехват в mangle+nat.
//
// policyMode=false («перехватывать всё») обнуляет то, что имеет смысл только
// внутри политики: DNS-RESCUE по LAN-мостам и ingress-MARK по интерфейсам.
func (s *ServiceImpl) buildTproxySpec(
	ctx context.Context,
	sr storage.SingboxRouterSettings,
	mark string,
	policyMode bool,
	wanIPs []string,
	qosSpecs []QoSClassSpec,
) RestoreInputSpec {
	var lanBridges []LANBridgeDNSRedir
	var ingress []string
	if policyMode {
		// Discover LAN bridges that NDMS knows how to REDIRECT DNS for
		// (i.e. has _NDM_HOTSPOT_DNSREDIR rules on). DNS-NOPOLICY rules
		// re-mark mark=0 DNS up to one of those marks so the existing
		// REDIRECT picks it up and forwards to the per-policy ndnproxy.
		// We pass our policy mark so the picker avoids it — re-marking
		// default DNS up to the sing-box mark would route it via Policy1's
		// (permit-less) table and DNS would never resolve. Empty result =
		// no qualifying bridges = skip the DNS-NOPOLICY logic entirely.
		lanBridges, _ = discoverLANBridges(ctx, mark)
		ingress = s.resolveIngressInterfaces(ctx, sr.IngressInterfaces)
	}
	bypassUDP, bypassTCP, _ := resolveBypassPorts(sr.BypassPresets, sr.BypassExtraPorts)
	// Адрес KeenDNS приходит с роутера, а не из настроек: без него в спеке его
	// появление (первый успешный запрос после старта) или смена не дали бы
	// переустановки правил, и обход доехал бы только по ручному Enable.
	bypassSubnets, _ := resolveBypassCIDRs(sr.BypassPresets, sr.BypassExtraSubnets, s.keenDNSBypass())
	return RestoreInputSpec{
		PolicyMark:        mark,
		MatchAll:          !policyMode,
		WANIPs:            wanIPs,
		LANBridges:        lanBridges,
		BypassUDPPorts:    bypassUDP,
		BypassTCPPorts:    bypassTCP,
		BypassCIDRs:       bypassSubnets,
		BypassGeoIPSet:    len(sr.BypassGeoIPTags) > 0,
		IngressInterfaces: ingress,
		QoSClasses:        qosSpecs,
	}
}

// buildPolicyTunSpec — режим policy-tun: netfilter нужен ТОЛЬКО под
// DSCP-диспатч классов QoS, основной трафик уводит NDMS-политика в tun.
// Отсюда DSCPOnly и MatchAll: цепочки содержат лишь bypass-RETURN'ы и
// диспатч, а сужать их меткой политики незачем — по метке уже отобрал NDMS.
// WAN-исключения обязательны и здесь: без них DSCP-меченный трафик на
// собственный адрес роутера ушёл бы в sing-box петлёй.
func (s *ServiceImpl) buildPolicyTunSpec(
	sr storage.SingboxRouterSettings,
	wanIPs []string,
	qosSpecs []QoSClassSpec,
) RestoreInputSpec {
	bypassUDP, bypassTCP, _ := resolveBypassPorts(sr.BypassPresets, sr.BypassExtraPorts)
	bypassSubnets, _ := resolveBypassCIDRs(sr.BypassPresets, sr.BypassExtraSubnets, s.keenDNSBypass())
	return RestoreInputSpec{
		DSCPOnly:       true,
		MatchAll:       true,
		WANIPs:         wanIPs,
		BypassUDPPorts: bypassUDP,
		BypassTCPPorts: bypassTCP,
		BypassCIDRs:    bypassSubnets,
		QoSClasses:     qosSpecs,
	}
}
