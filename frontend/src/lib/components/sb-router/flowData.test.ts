import { describe, it, expect } from 'vitest';
import { deriveRoutingSummary, formatWanInterfaceLabel, resolveDefaultWanLabel } from './flowData';
import type { SingboxRouterWANInterface } from '$lib/types';
import type { SingboxRouterRule, SingboxRouterDNSServer, SingboxRouterDNSGlobals } from '$lib/types';
import type { OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';

const dnsServers: SingboxRouterDNSServer[] = [
  { tag: 'dns-direct', type: 'udp', server: '77.88.8.8' },
  { tag: 'dns-tunnel', type: 'udp', server: '9.9.9.9', detour: 'my-selector' },
];
const globals: SingboxRouterDNSGlobals = { final: 'dns-direct', strategy: 'ipv4_only' };
const outboundOptions: OutboundGroup[] = [
  { group: 'AWG туннели', items: [{ value: 'awg-awg10', label: 'Office VPN (t2s10)' }] },
];

const wanIfaces: SingboxRouterWANInterface[] = [
  { name: 'ppp0', id: '1', label: 'Beeline', up: true, priority: 1 },
  { name: 'eth3', id: '2', label: 'Ethernet', up: false, priority: 2 },
];

describe('resolveDefaultWanLabel', () => {
  it('manual WAN → label (interface)', () => {
    expect(
      resolveDefaultWanLabel({ wanAutoDetect: false, wanInterface: 'ppp0' }, wanIfaces, 'direct'),
    ).toBe('Beeline (ppp0)');
  });

  it('auto-detect → primary up WAN by priority', () => {
    expect(resolveDefaultWanLabel({ wanAutoDetect: true, wanInterface: '' }, wanIfaces, 'direct')).toBe(
      'Beeline (ppp0)',
    );
  });

  it('skipped when route final is not direct', () => {
    expect(
      resolveDefaultWanLabel({ wanAutoDetect: false, wanInterface: 'ppp0' }, wanIfaces, 'wg-nl'),
    ).toBeNull();
  });
});

describe('formatWanInterfaceLabel', () => {
  it('falls back to interface name when label empty', () => {
    expect(formatWanInterfaceLabel({ name: 'ppp0', label: '' })).toBe('ppp0 (ppp0)');
  });
});

describe('deriveRoutingSummary', () => {
  it('1 туннель + 2 сервиса: defaultLabel Напрямую, dns по веткам', () => {
    const rules: SingboxRouterRule[] = [
      { rule_set: ['geosite-netflix'], outbound: 'my-selector', action: 'route' },
      { rule_set: ['geosite-youtube'], outbound: 'my-selector', action: 'route' },
    ];
    const s = deriveRoutingSummary(rules, 'direct', dnsServers, globals);
    expect(s.defaultLabel).toBe('Напрямую');
    expect(s.defaultDnsLabel).toBe('77.88.8.8');
    expect(s.tunnels).toEqual(['my-selector']);
    expect(s.tunneledRuleCount).toBe(2);
    expect(s.bypassRuleCount).toBe(0);
    expect(s.tunnelDnsLabel).toBe('9.9.9.9');
  });

  it('reject и direct правила не считаются туннелируемыми', () => {
    const rules: SingboxRouterRule[] = [
      { rule_set: ['ads'], action: 'reject' },
      { domain_suffix: ['x.com'], outbound: 'direct', action: 'route' },
      { rule_set: ['svc'], outbound: 'wg-nl', action: 'route' },
    ];
    const s = deriveRoutingSummary(rules, 'direct', dnsServers, globals);
    expect(s.tunnels).toEqual(['wg-nl']);
    expect(s.tunneledRuleCount).toBe(1);
    expect(s.bypassRuleCount).toBe(1);
  });

  it('>1 туннель: оба тега, общий счётчик', () => {
    const rules: SingboxRouterRule[] = [
      { rule_set: ['a'], outbound: 'wg-nl', action: 'route' },
      { rule_set: ['b'], outbound: 'wg-us', action: 'route' },
      { rule_set: ['c'], outbound: 'wg-nl', action: 'route' },
    ];
    const s = deriveRoutingSummary(rules, 'direct', dnsServers, globals);
    expect(s.tunnels).toEqual(['wg-nl', 'wg-us']);
    expect(s.tunneledRuleCount).toBe(3);
    expect(s.bypassRuleCount).toBe(0);
  });

  it('final не dns-direct / нет detour-сервера → системный DNS / null', () => {
    const s = deriveRoutingSummary([], 'direct', [{ tag: 'dns-bootstrap', type: 'udp', server: '1.1.1.1' }], { final: 'dns-bootstrap', strategy: 'ipv4_only' });
    expect(s.defaultDnsLabel).toBe('1.1.1.1');
    expect(s.tunnelDnsLabel).toBeNull();
    expect(s.tunnels).toEqual([]);
  });

  it('подписывает AWG tunnel в summary по каталогу, а не raw tag', () => {
    const rules: SingboxRouterRule[] = [
      { rule_set: ['svc'], outbound: 'awg-awg10', action: 'route' },
    ];
    const s = deriveRoutingSummary(rules, 'awg-awg10', dnsServers, globals, outboundOptions);
    expect(s.defaultLabel).toBe('Office VPN (t2s10)');
    expect(s.tunnels).toEqual(['Office VPN (t2s10)']);
  });

  it('отдаёт теги DNS-серверов обеих веток', () => {
    const s = deriveRoutingSummary([], 'direct', dnsServers, globals);
    expect(s.defaultDnsTag).toBe('dns-direct');
    expect(s.tunnelDnsTag).toBe('dns-tunnel');
  });

  it('нет сервера под final / нет detour-сервера → теги null', () => {
    const s = deriveRoutingSummary([], 'direct', [], { final: 'dns-gone', strategy: 'ipv4_only' });
    expect(s.defaultDnsTag).toBeNull();
    expect(s.tunnelDnsTag).toBeNull();
    expect(s.defaultDnsLabel).toBe('системный');
  });
});
