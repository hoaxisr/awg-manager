import type { SingboxRouterRule, SingboxRouterDNSServer, SingboxRouterDNSGlobals } from '$lib/types';
import type { OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';

export interface RoutingSummary {
  /** Подпись выхода по умолчанию: «Напрямую» если route.final = direct, иначе тег. */
  defaultLabel: string;
  /** DNS для ветки по умолчанию: server final-сервера, иначе «системный». */
  defaultDnsLabel: string;
  /** Уникальные теги туннельных outbound'ов, используемых правилами (в порядке появления). */
  tunnels: string[];
  /** Кол-во туннелируемых правил. */
  tunneledRuleCount: number;
  /** DNS туннельной ветки: server первого detour-сервера, иначе null. */
  tunnelDnsLabel: string | null;
}

function isTunneled(r: SingboxRouterRule): boolean {
  return !!r.outbound && r.outbound !== 'direct' && r.action !== 'reject';
}

function dnsLabelByTag(servers: SingboxRouterDNSServer[], tag: string): string | null {
  const s = servers.find((x) => x.tag === tag);
  if (!s) return null;
  return s.server || s.tag;
}

function outboundLabelByTag(groups: OutboundGroup[] | undefined, tag: string): string {
  for (const group of groups ?? []) {
    const item = group.items?.find((x) => x.value === tag);
    if (item) return item.label;
  }
  return tag;
}

export function deriveRoutingSummary(
  rules: SingboxRouterRule[],
  routeFinal: string,
  dnsServers: SingboxRouterDNSServer[],
  dnsGlobals: SingboxRouterDNSGlobals,
  outboundOptions: OutboundGroup[] = [],
): RoutingSummary {
  const tunneled = rules.filter(isTunneled);
  const tunnels: string[] = [];
  for (const r of tunneled) {
    const tag = r.outbound as string;
    if (!tunnels.includes(tag)) tunnels.push(tag);
  }

  const defaultLabel = routeFinal && routeFinal !== 'direct' ? outboundLabelByTag(outboundOptions, routeFinal) : 'Напрямую';
  const defaultDnsLabel = dnsLabelByTag(dnsServers, dnsGlobals.final) ?? 'системный';

  const detourServer = dnsServers.find((s) => !!s.detour);
  const tunnelDnsLabel = detourServer ? (detourServer.server || detourServer.tag) : null;

  return {
    defaultLabel,
    defaultDnsLabel,
    tunnels: tunnels.map((tag) => outboundLabelByTag(outboundOptions, tag)),
    tunneledRuleCount: tunneled.length,
    tunnelDnsLabel,
  };
}
