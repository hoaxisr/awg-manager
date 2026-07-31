import type {
	SingboxRouterRule,
	SingboxRouterDNSServer,
	SingboxRouterDNSGlobals,
	SingboxRouterSettings,
	SingboxRouterWANInterface,
} from '$lib/types';
import type { OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';
import { isSystemRule } from './adapters';

export interface RoutingSummary {
  /** Подпись выхода по умолчанию: «Напрямую» если route.final = direct, иначе тег. */
  defaultLabel: string;
  /** DNS для ветки по умолчанию: server final-сервера, иначе «системный». */
  defaultDnsLabel: string;
  /** Тег сервера под dns.final — для перехода в редактор. null, если сервера нет. */
  defaultDnsTag: string | null;
  /** Уникальные теги туннельных outbound'ов, используемых правилами (в порядке появления). */
  tunnels: string[];
  /** Кол-во туннелируемых правил. */
  tunneledRuleCount: number;
  /** Кол-во правил, идущих мимо туннеля (direct / final). */
  bypassRuleCount: number;
  /** DNS туннельной ветки: server первого detour-сервера, иначе null. */
  tunnelDnsLabel: string | null;
  /** Тег первого detour-сервера. null, если такого нет. */
  tunnelDnsTag: string | null;
}

// Конвенция тега туннельного DNS-сервера (см. emptyStateActions.ts). Своя
// константа: импорт между модулями сюда не нужен.
const DNS_TUNNEL_TAG = 'dns-tunnel';

function isTunneled(r: SingboxRouterRule): boolean {
  return !!r.outbound && r.outbound !== 'direct' && r.action !== 'reject';
}

function isBypassTunnel(r: SingboxRouterRule): boolean {
  if (r.action === 'reject' || isSystemRule(r)) return false;
  return !isTunneled(r);
}

function outboundLabelByTag(groups: OutboundGroup[] | undefined, tag: string): string {
  for (const group of groups ?? []) {
    const item = group.items?.find((x) => x.value === tag);
    if (item) return item.label;
  }
  return tag;
}

export function formatWanInterfaceLabel(
	iface: Pick<SingboxRouterWANInterface, 'name' | 'label'>,
): string {
	const label = iface.label?.trim() || iface.name;
	return `${label} (${iface.name})`;
}

/** Подпись WAN для ветки «напрямую»: выбранный или авто-определяемый интерфейс провайдера. */
export function resolveDefaultWanLabel(
	settings: Pick<SingboxRouterSettings, 'wanAutoDetect' | 'wanInterface'> | null | undefined,
	wanInterfaces: SingboxRouterWANInterface[],
	routeFinal = 'direct',
): string | null {
	if (routeFinal && routeFinal !== 'direct') return null;
	if (!settings) return null;

	if (!settings.wanAutoDetect) {
		const name = settings.wanInterface?.trim();
		if (!name) return null;
		const iface = wanInterfaces.find((i) => i.name === name);
		return iface ? formatWanInterfaceLabel(iface) : formatWanInterfaceLabel({ name, label: name });
	}

	const sorted = [...wanInterfaces].sort((a, b) => a.priority - b.priority);
	const primary = sorted.find((i) => i.up) ?? sorted[0];
	return primary ? formatWanInterfaceLabel(primary) : null;
}

export function deriveRoutingSummary(
  rules: SingboxRouterRule[],
  routeFinal: string,
  dnsServers: SingboxRouterDNSServer[],
  dnsGlobals: SingboxRouterDNSGlobals,
  outboundOptions: OutboundGroup[] = [],
): RoutingSummary {
  const tunneled = rules.filter(isTunneled);
  const bypass = rules.filter(isBypassTunnel);
  const tunnels: string[] = [];
  for (const r of tunneled) {
    const tag = r.outbound as string;
    if (!tunnels.includes(tag)) tunnels.push(tag);
  }

  const defaultLabel = routeFinal && routeFinal !== 'direct' ? outboundLabelByTag(outboundOptions, routeFinal) : 'Напрямую';
  const finalServer = dnsServers.find((x) => x.tag === dnsGlobals.final);
  const defaultDnsLabel = finalServer ? finalServer.server || finalServer.tag : 'системный';

  // На легаси-конфигах detour может висеть на dns-direct — тег dns-tunnel
  // приоритетнее первого сервера с detour.
  const detourServer = dnsServers.find((s) => s.tag === DNS_TUNNEL_TAG) ?? dnsServers.find((s) => !!s.detour);
  const tunnelDnsLabel = detourServer ? (detourServer.server || detourServer.tag) : null;

  return {
    defaultLabel,
    defaultDnsLabel,
    defaultDnsTag: finalServer?.tag ?? null,
    tunnels: tunnels.map((tag) => outboundLabelByTag(outboundOptions, tag)),
    tunneledRuleCount: tunneled.length,
    bypassRuleCount: bypass.length,
    tunnelDnsLabel,
    tunnelDnsTag: detourServer?.tag ?? null,
  };
}
