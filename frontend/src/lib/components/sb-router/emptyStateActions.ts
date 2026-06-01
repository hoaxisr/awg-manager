import { api } from '$lib/api/client';
import { singboxRouter } from '$lib/stores/singboxRouter';
import type { SingboxRouterDNSServer, SingboxRouterRule, SingboxRouterDNSRule } from '$lib/types';
import type { CustomMatcherFields } from './addWizardStore';
import type { TemplateGroup } from './templatesData';
import type { SubmitResult } from './templatesActions';
import { submitWizard } from './addWizardActions';
import { mergeAndSaveSettings } from './settingsActions';

export interface FinishSetupArgs {
  tunnelTag: string;
  selectedTemplates: string[];
  customFields: CustomMatcherFields;
  groups: TemplateGroup[];
  existingRuleSetTags: string[];
}

export async function finishSetup(args: FinishSetupArgs): Promise<SubmitResult> {
  const result = await submitWizard({
    selectedTemplates: args.selectedTemplates,
    customFields: args.customFields,
    outboundCategory: 'tunnel',
    tunnelTag: args.tunnelTag,
    groups: args.groups,
    existingRuleSetTags: args.existingRuleSetTags,
  });
  try {
    await ensureTunnelDnsInfra(args.tunnelTag);
    await syncTunnelDnsRule();
  } catch (e) {
    result.failures.push({ id: 'dns', error: e instanceof Error ? e.message : String(e) });
  }
  await api.singboxRouterPutRouteFinal('direct');
  await mergeAndSaveSettings({
    deviceMode: 'all',
    wanAutoDetect: true,
    wanInterface: '',
    snifferEnabled: true,
  });
  await api.singboxRouterEnable();
  await singboxRouter.loadAll();
  return result;
}

const DNS_DIRECT_TAG = 'dns-direct';
const DNS_TUNNEL_TAG = 'dns-tunnel';

export async function ensureTunnelDnsInfra(tunnelTag: string): Promise<void> {
  const servers = await api.singboxRouterListDNSServers();
  const tags = new Set(servers.map((s) => s.tag));
  if (!tags.has(DNS_DIRECT_TAG)) {
    await api.singboxRouterAddDNSServer({ tag: DNS_DIRECT_TAG, type: 'udp', server: '77.88.8.8' });
  }
  const tunnelServer: SingboxRouterDNSServer = { tag: DNS_TUNNEL_TAG, type: 'udp', server: '9.9.9.9', detour: tunnelTag };
  if (tags.has(DNS_TUNNEL_TAG)) {
    await api.singboxRouterUpdateDNSServer(DNS_TUNNEL_TAG, tunnelServer);
  } else {
    await api.singboxRouterAddDNSServer(tunnelServer);
  }
  const globals = await api.singboxRouterGetDNSGlobals();
  await api.singboxRouterPutDNSGlobals({ final: DNS_DIRECT_TAG, strategy: globals.strategy || 'prefer_ipv4' });
}

function collectTunnelDomainMatchers(rules: SingboxRouterRule[]) {
  const rs = new Set<string>(), ds = new Set<string>();
  for (const r of rules) {
    if (!r.outbound || r.outbound === 'direct' || r.action === 'reject') continue;
    (r.rule_set ?? []).forEach((x) => rs.add(x));
    (r.domain_suffix ?? []).forEach((x) => ds.add(x));
  }
  return { rule_set: [...rs], domain_suffix: [...ds] };
}

/** Пересчитывает ОДНО dns-tunnel DNS-правило из всех не-direct routing-правил. */
export async function syncTunnelDnsRule(): Promise<void> {
  const rules = await api.singboxRouterListRules();
  const agg = collectTunnelDomainMatchers(rules);
  const hasAny = agg.rule_set.length || agg.domain_suffix.length;

  const servers = await api.singboxRouterListDNSServers();
  const hasTunnelServer = servers.some((s) => s.tag === DNS_TUNNEL_TAG);
  const dnsRules = await api.singboxRouterListDNSRules();
  const tunnelIdx: number[] = [];
  dnsRules.forEach((r, i) => { if (r.server === DNS_TUNNEL_TAG) tunnelIdx.push(i); });

  if (!hasTunnelServer) {
    for (let k = tunnelIdx.length - 1; k >= 0; k--) {
      await api.singboxRouterDeleteDNSRule(tunnelIdx[k]);
    }
    return;
  }

  if (!hasAny) {
    for (let k = tunnelIdx.length - 1; k >= 0; k--) {
      await api.singboxRouterDeleteDNSRule(tunnelIdx[k]);
    }
    return;
  }

  const desired: SingboxRouterDNSRule = { server: DNS_TUNNEL_TAG };
  if (agg.rule_set.length) desired.rule_set = agg.rule_set;
  if (agg.domain_suffix.length) desired.domain_suffix = agg.domain_suffix;

  if (tunnelIdx.length === 0) {
    await api.singboxRouterAddDNSRule(desired);
  } else {
    await api.singboxRouterUpdateDNSRule(tunnelIdx[0], desired);
    for (let k = tunnelIdx.length - 1; k >= 1; k--) {
      await api.singboxRouterDeleteDNSRule(tunnelIdx[k]);
    }
  }
}
