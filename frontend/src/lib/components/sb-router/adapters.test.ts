import { describe, it, expect } from 'vitest';
import { singboxRuleToCard, resolveOutboundDisplay, extractMatcherChips, isSystemRule } from './adapters';
import type { SingboxRouterRule, SingboxRouterOutbound } from '$lib/types';
import type { OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';

const noOutbounds: SingboxRouterOutbound[] = [];
const noRulesets: Record<string, string> = {};
const awgOptions: OutboundGroup[] = [
  { group: 'AWG туннели', items: [{ value: 'awg-awg10', label: 'Office VPN (t2s10)' }] },
];

describe('isSystemRule', () => {
  it('ip_is_private with direct outbound is system', () => {
    expect(isSystemRule({ ip_is_private: true, outbound: 'direct' })).toBe(true);
  });
  it('action sniff is system', () => {
    expect(isSystemRule({ action: 'sniff' })).toBe(true);
  });
  it('action hijack-dns is system', () => {
    expect(isSystemRule({ action: 'hijack-dns' })).toBe(true);
  });
  it('regular routing rule is not system', () => {
    expect(isSystemRule({ domain_suffix: ['netflix.com'], outbound: 'warp' })).toBe(false);
  });
});

describe('resolveOutboundDisplay', () => {
  it('direct outbound', () => {
    const d = resolveOutboundDisplay('direct', 'route', noOutbounds);
    expect(d).toEqual({ name: 'direct', label: 'Прямо', kind: 'direct' });
  });

  it('block outbound', () => {
    const d = resolveOutboundDisplay('block', 'route', noOutbounds);
    expect(d).toEqual({ name: 'block', label: 'Блок', kind: 'block' });
  });

  it('reject outbound aliased to block', () => {
    const d = resolveOutboundDisplay('reject', 'route', noOutbounds);
    expect(d).toEqual({ name: 'reject', label: 'Блок', kind: 'block' });
  });

  it('action=block returns block regardless of outbound name', () => {
    const d = resolveOutboundDisplay('warp', 'block', noOutbounds);
    expect(d.kind).toBe('block');
    expect(d.label).toBe('Блок');
  });

  it('known outbound by name', () => {
    const ob: SingboxRouterOutbound[] = [
      { tag: 'warp', type: 'wireguard' } as unknown as SingboxRouterOutbound,
    ];
    const d = resolveOutboundDisplay('warp', 'route', ob);
    expect(d.name).toBe('warp');
    expect(d.label).toBe('warp');
    expect(d.kind).toBe('tunnel');
  });

  it('composite outbound (selector)', () => {
    const ob: SingboxRouterOutbound[] = [
      { tag: 'group-1', type: 'selector' } as unknown as SingboxRouterOutbound,
    ];
    const d = resolveOutboundDisplay('group-1', 'route', ob);
    expect(d.kind).toBe('composite');
  });

  it('unknown outbound by name', () => {
    const d = resolveOutboundDisplay('mystery', 'route', noOutbounds);
    expect(d).toEqual({ name: 'mystery', label: 'mystery', kind: 'unknown' });
  });

  it('AWG outbound tag uses catalog label and AWG visual kind', () => {
    const d = resolveOutboundDisplay('awg-awg10', 'route', noOutbounds, awgOptions);
    expect(d).toEqual({ name: 'awg-awg10', label: 'Office VPN (t2s10)', kind: 'awg' });
  });

  it('undefined outbound → direct fallback', () => {
    const d = resolveOutboundDisplay(undefined, 'route', noOutbounds);
    expect(d.kind).toBe('direct');
    expect(d.name).toBe('direct');
  });

  it('sniff action renders SNIFF mono badge', () => {
    const d = resolveOutboundDisplay(undefined, 'sniff', noOutbounds);
    expect(d.kind).toBe('sniff');
    expect(d.label).toBe('SNIFF');
  });

  it('hijack-dns action renders HIJACK-DNS mono badge', () => {
    const d = resolveOutboundDisplay(undefined, 'hijack-dns', noOutbounds);
    expect(d.kind).toBe('hijack-dns');
    expect(d.label).toBe('HIJACK-DNS');
  });
});

describe('extractMatcherChips', () => {
  it('empty rule has no chips', () => {
    expect(extractMatcherChips({}, noRulesets)).toEqual([]);
  });

  it('domain_suffix → domain chips', () => {
    const chips = extractMatcherChips({ domain_suffix: ['netflix.com', 'nflxext.com'] }, noRulesets);
    expect(chips).toEqual([
      { kind: 'domain', label: 'netflix.com' },
      { kind: 'domain', label: 'nflxext.com' },
    ]);
  });

  it('ip_cidr → ip chips with mono', () => {
    const chips = extractMatcherChips({ ip_cidr: ['10.0.0.0/8'] }, noRulesets);
    expect(chips).toEqual([{ kind: 'ip', label: '10.0.0.0/8', mono: true }]);
  });

  it('source_ip_cidr → src chips with mono', () => {
    const chips = extractMatcherChips({ source_ip_cidr: ['192.168.1.0/24'] }, noRulesets);
    expect(chips).toEqual([{ kind: 'src', label: '192.168.1.0/24', mono: true }]);
  });

  it('port → port chips with mono', () => {
    const chips = extractMatcherChips({ port: [443, 8443] }, noRulesets);
    expect(chips).toEqual([
      { kind: 'port', label: '443', mono: true },
      { kind: 'port', label: '8443', mono: true },
    ]);
  });

  it('rule_set → ruleset chips with label lookup', () => {
    const rulesets = { 'geoip-ru': 'Россия (GeoIP)' };
    const chips = extractMatcherChips({ rule_set: ['geoip-ru', 'unknown-set'] }, rulesets);
    expect(chips).toEqual([
      { kind: 'ruleset', label: 'Россия (GeoIP)' },
      { kind: 'ruleset', label: 'unknown-set' },
    ]);
  });

  it('protocol → protocol chip', () => {
    const chips = extractMatcherChips({ protocol: 'quic' }, noRulesets);
    expect(chips).toEqual([{ kind: 'protocol', label: 'quic' }]);
  });

  it('ip_is_private → private chip', () => {
    const chips = extractMatcherChips({ ip_is_private: true }, noRulesets);
    expect(chips).toEqual([{ kind: 'private', label: 'Локальная сеть' }]);
  });

  it('combined matchers', () => {
    const chips = extractMatcherChips({
      domain_suffix: ['netflix.com'],
      port: [443],
    }, noRulesets);
    expect(chips).toHaveLength(2);
    expect(chips[0].kind).toBe('domain');
    expect(chips[1].kind).toBe('port');
  });
});

describe('singboxRuleToCard', () => {
  it('produces a netflix card', () => {
    const card = singboxRuleToCard(
      { domain_suffix: ['netflix.com'], outbound: 'warp' },
      0,
      [{ tag: 'warp', type: 'wireguard' } as unknown as SingboxRouterOutbound],
      {},
    );
    expect(card.serviceKey).toBe('netflix');
    expect(card.title).toMatch(/netflix/i);
    expect(card.action).toBe('route');
    expect(card.outbound.kind).toBe('tunnel');
    expect(card.outbound.label).toBe('warp');
    expect(card.isSystem).toBe(false);
    expect(card.matchers).toHaveLength(1);
    expect(card.matchers[0].kind).toBe('domain');
  });

  it('renders AWG rule destination as tunnel name instead of raw tag', () => {
    const card = singboxRuleToCard(
      { domain_suffix: ['example.com'], outbound: 'awg-awg10' },
      0,
      [],
      {},
      [],
      awgOptions,
    );
    expect(card.outbound.kind).toBe('awg');
    expect(card.outbound.label).toBe('Office VPN (t2s10)');
  });

  it('produces a system rule card', () => {
    const card = singboxRuleToCard(
      { ip_is_private: true, outbound: 'direct' },
      0, [], {},
    );
    expect(card.isSystem).toBe(true);
    expect(card.outbound.kind).toBe('direct');
    expect(card.title).toMatch(/локальн/i);
  });

  it('maps reject action to block', () => {
    const card = singboxRuleToCard(
      { action: 'reject', domain_suffix: ['ads.example.com'] },
      0, [], {},
    );
    expect(card.action).toBe('block');
    expect(card.outbound.kind).toBe('block');
  });

  it('omitted action defaults to route', () => {
    const card = singboxRuleToCard(
      { domain_suffix: ['example.com'], outbound: 'warp' },
      0,
      [{ tag: 'warp', type: 'wireguard' } as unknown as SingboxRouterOutbound],
      {},
    );
    expect(card.action).toBe('route');
  });

  it('outbound=direct sets action=direct', () => {
    const card = singboxRuleToCard(
      { domain_suffix: ['example.com'], outbound: 'direct' },
      0, [], {},
    );
    expect(card.action).toBe('direct');
  });

  it('generates a stable id for the same input', () => {
    const r: SingboxRouterRule = { domain_suffix: ['netflix.com'], outbound: 'warp' };
    const a = singboxRuleToCard(r, 0, [], {});
    const b = singboxRuleToCard(r, 0, [], {});
    expect(a.id).toBe(b.id);
  });

  it('sniff system rule — title/subtitle по дизайну', () => {
    const card = singboxRuleToCard({ action: 'sniff' }, 0, [], {});
    expect(card.isSystem).toBe(true);
    expect(card.title).toBe('Анализ протокола');
    expect(card.subtitle).toBe('sniff');
    expect(card.action).toBe('sniff');
    expect(card.outbound.kind).toBe('sniff');
    expect(card.outbound.label).toBe('SNIFF');
  });

  it('hijack-dns system rule — title/subtitle по дизайну', () => {
    const card = singboxRuleToCard({ action: 'hijack-dns' }, 0, [], {});
    expect(card.isSystem).toBe(true);
    expect(card.title).toBe('Перехват DNS');
    expect(card.subtitle).toBe('protocol=dns OR port=53');
    expect(card.action).toBe('hijack-dns');
    expect(card.outbound.kind).toBe('hijack-dns');
    expect(card.outbound.label).toBe('HIJACK-DNS');
  });

  it('ip_is_private bypass — subtitle с RFC1918', () => {
    const card = singboxRuleToCard({ ip_is_private: true, outbound: 'direct' }, 0, [], {});
    expect(card.title).toBe('Локальная сеть');
    expect(card.subtitle).toBe('RFC1918 · loopback · link-local · CGNAT');
  });

  it('resolves icon and title from geosite rule_set via router presets', () => {
    const presets = [{
      id: 'openai',
      name: 'OpenAI',
      iconSlug: 'openai',
      ruleSets: [{ tag: 'geosite-openai', url: 'https://example/openai.srs' }],
      rules: [{ ruleSetRef: 'geosite-openai', actionTarget: 'tunnel' as const }],
    }];
    const card = singboxRuleToCard(
      { rule_set: ['geosite-openai'], outbound: 'warp' },
      0,
      [{ tag: 'warp', type: 'wireguard' } as unknown as SingboxRouterOutbound],
      {},
      presets,
    );
    expect(card.serviceKey).toBe('openai');
    expect(card.title).toBe('OpenAI');
  });
});
