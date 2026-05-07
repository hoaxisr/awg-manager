// Family taxonomy for subgroup pill colors. Edit here to recategorize.
export type SubgroupFamily =
  | 'routing'
  | 'lifecycle'
  | 'network'
  | 'policy'
  | 'system'
  | 'feature'
  | 'singbox-runtime';

export const SUBGROUP_FAMILY: Record<string, SubgroupFamily> = {
  // routing
  'dns-route': 'routing',
  'static-route': 'routing',
  'client-route': 'routing',
  'singbox-router': 'routing',
  'hrneo': 'routing',
  // lifecycle
  'lifecycle': 'lifecycle',
  'ops': 'lifecycle',
  'state': 'lifecycle',
  'boot': 'lifecycle',
  'cleanup': 'lifecycle',
  // network
  'connectivity': 'network',
  'pingcheck': 'network',
  'dnscheck': 'network',
  'wan': 'network',
  'connections': 'network',
  'traffic': 'network',
  // policy
  'access-policy': 'policy',
  'firewall': 'policy',
  'signature': 'policy',
  // system
  'rci': 'system',
  'ndms': 'system',
  'settings': 'system',
  'auth': 'system',
  'update': 'system',
  'system-tunnels': 'system',
  // feature
  'deviceproxy': 'feature',
  'managed': 'feature',
  'awg-outbounds': 'feature',
  'catalog': 'feature',
  'test': 'feature',
  'diagnostics': 'feature',
  // singbox-runtime (singbox-bucket only)
  'inbound': 'singbox-runtime',
  'outbound': 'singbox-runtime',
  'dns': 'singbox-runtime',
  'router': 'singbox-runtime',
  'runtime': 'singbox-runtime',
  'process': 'singbox-runtime',
};

export function familyOf(subgroup: string | undefined | null): SubgroupFamily | null {
  if (!subgroup) return null;
  return SUBGROUP_FAMILY[subgroup] ?? null;
}
