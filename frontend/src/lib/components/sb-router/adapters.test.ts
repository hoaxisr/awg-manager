import { describe, it, expect } from 'vitest';
import {
  singboxRuleToCard,
  resolveOutboundDisplay,
  extractMatcherChips,
  extractMatchersForCard,
  firstDomainFromInlineRules,
  isSystemRule,
  mapRuleAction,
  systemRuleTooltip,
} from './adapters';
import { classifyRuleSimplicity } from './simpleRule';
import type { SingboxRouterRule, SingboxRouterOutbound, CatalogPreset } from '$lib/types';
import type { OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';

const noOutbounds: SingboxRouterOutbound[] = [];
const noRulesets: Record<string, string> = {};
const awgOptions: OutboundGroup[] = [
  { group: 'AWG туннели', items: [{ value: 'awg-awg10', label: 'Office VPN (nwg0)' }] },
];

const catalog: CatalogPreset[] = [
  {
    id: 'netflix',
    name: 'Netflix',
    iconSlug: 'netflix',
    category: 'media',
    origin: 'builtin',
    engines: { dns: { domains: ['netflix.com', 'nflxext.com'] } },
  },
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

// Bulk-выбор правил (RulesPanel/ExpertPanel/RoutesTab) строится по одному и
// тому же предикату `!isSystem && (action === 'route' || action === 'direct')`
// поверх isSystemRule + mapRuleAction. Тестируем сами строительные блоки,
// чтобы регресс #558 fix-волна 2 (Minor 3: direct-правила исключались) не
// повторился — «Локальная сеть» и reject должны остаться невыбираемыми.
describe('bulk rule selectability building blocks', () => {
  function isSelectable(rule: SingboxRouterRule): boolean {
    const action = mapRuleAction(rule);
    return !isSystemRule(rule) && (action === 'route' || action === 'direct');
  }

  it('user rule routed to a tunnel outbound is selectable', () => {
    expect(isSelectable({ domain_suffix: ['netflix.com'], outbound: 'warp' })).toBe(true);
  });

  it('user rule with direct outbound is selectable', () => {
    expect(isSelectable({ domain_suffix: ['example.com'], outbound: 'direct' })).toBe(true);
  });

  it('"Локальная сеть" (ip_is_private + direct) stays non-selectable', () => {
    expect(isSelectable({ ip_is_private: true, outbound: 'direct' })).toBe(false);
  });

  it('block/reject rule stays non-selectable', () => {
    expect(isSelectable({ action: 'reject', domain_suffix: ['ads.example.com'] })).toBe(false);
  });

  it('sniff/hijack-dns system rules stay non-selectable', () => {
    expect(isSelectable({ action: 'sniff' })).toBe(false);
    expect(isSelectable({ action: 'hijack-dns' })).toBe(false);
  });
});

describe('systemRuleTooltip', () => {
  it('sniff rule has tooltip', () => {
    expect(systemRuleTooltip({ action: 'sniff' })).toMatch(/SNI/);
  });
  it('hijack-dns rule has tooltip', () => {
    expect(systemRuleTooltip({ action: 'hijack-dns' })).toMatch(/DNS/);
  });
  it('private bypass rule has tooltip', () => {
    expect(systemRuleTooltip({ ip_is_private: true, outbound: 'direct' })).toMatch(/LAN/);
  });
  it('regular rule has no tooltip', () => {
    expect(systemRuleTooltip({ domain_suffix: ['x.com'], outbound: 'warp' })).toBeUndefined();
  });
});

describe('resolveOutboundDisplay', () => {
  it('direct outbound', () => {
    const d = resolveOutboundDisplay('direct', 'route', noOutbounds);
    expect(d).toEqual({ name: 'direct', label: 'Прямо', kind: 'direct', tone: 'direct' });
  });

  it('block outbound', () => {
    const d = resolveOutboundDisplay('block', 'route', noOutbounds);
    expect(d).toEqual({ name: 'block', label: 'Блок', kind: 'block', tone: 'block' });
  });

  it('reject outbound aliased to block', () => {
    const d = resolveOutboundDisplay('reject', 'route', noOutbounds);
    expect(d).toEqual({ name: 'reject', label: 'Блок', kind: 'block', tone: 'block' });
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
    expect(d.tone).toBe('proxy');
  });

  it('composite outbound (selector) shows active member and others', () => {
    const ob: SingboxRouterOutbound[] = [
      {
        tag: 'group-1',
        type: 'selector',
        outbounds: ['node-a', 'node-b'],
        source: 'router',
      },
    ];
    const d = resolveOutboundDisplay('group-1', 'route', ob, [], null, [
      { tag: 'group-1', type: 'selector', now: 'node-b', members: [] },
    ]);
    expect(d.kind).toBe('composite');
    expect(d.tone).toBe('composite');
    expect(d.activeMemberLabel).toBe('node-b');
    expect(d.otherMemberLabels).toEqual(['node-a']);
    expect(d.compositeType).toBe('selector');
  });

  it('composite active member gets proxyInterface suffix from singbox tunnels', () => {
    const ob: SingboxRouterOutbound[] = [
      {
        tag: 'group-1',
        type: 'selector',
        outbounds: ['vless-nl', 'vless-de'],
        source: 'router',
      },
    ];
    const tunnels = [{
      tag: 'vless-nl',
      protocol: 'vless' as const,
      server: 'x',
      port: 443,
      security: 'tls' as const,
      transport: 'tcp' as const,
      listenPort: 11000,
      proxyInterface: 't2s1',
      connectivity: { connected: true, latency: 50 },
      running: true,
    }];
    const d = resolveOutboundDisplay('group-1', 'route', ob, [], null, [
      { tag: 'group-1', type: 'selector', now: 'vless-nl', members: [] },
    ], tunnels);
    expect(d.activeMemberMetaSuffix).toBe('t2s1');
  });

  it('subscription composite without outbound entry still expands members', () => {
    const subs = [{
      id: 'sub-x',
      label: 'My Sub',
      url: '',
      isInline: false,
      headers: [],
      refreshHours: 24,
      lastFetched: '',
      selectorTag: 'sub-x',
      inboundTag: 'sub-x-in',
      listenPort: 11000,
      proxyIndex: 1,
      memberTags: ['sub-x-a'],
      members: [{ tag: 'sub-x-a', protocol: 'vless', server: 'host.example', port: 443 }],
      orphanTags: [],
      activeMember: 'sub-x-a',
      enabled: true,
      mode: 'selector' as const,
    }];
    const d = resolveOutboundDisplay('sub-x', 'route', [], [], subs);
    expect(d.kind).toBe('subscription');
    expect(d.tone).toBe('subscription');
    expect(d.activeMemberLabel).toBe('host.example');
    expect(d.label).toBe('My Sub');
    expect(d.metaSuffix).toBe('sub');
    expect(d.otherMemberLabels).toEqual([]);
  });

  it('solo sing-box proxy appends proxyInterface', () => {
    const tunnels = [{
      tag: 'vless-nl',
      protocol: 'vless' as const,
      server: 'x',
      port: 443,
      security: 'tls' as const,
      transport: 'tcp' as const,
      listenPort: 11000,
      proxyInterface: 't2s1',
      connectivity: { connected: true, latency: 50 },
      running: true,
    }];
    const opts = [{ group: 'Sing-box туннели', items: [{ value: 'vless-nl', label: 'vless-nl' }] }];
    const d = resolveOutboundDisplay('vless-nl', 'route', [], opts, null, [], tunnels);
    expect(d.label).toBe('vless-nl');
    expect(d.metaSuffix).toBe('t2s1');
    expect(d.kind).toBe('proxy');
  });

  it('unknown outbound by name', () => {
    const d = resolveOutboundDisplay('mystery', 'route', noOutbounds);
    expect(d).toEqual({ name: 'mystery', label: 'mystery', kind: 'unknown', tone: 'unknown' });
  });

  it('AWG outbound tag uses catalog label and AWG visual kind', () => {
    const d = resolveOutboundDisplay('awg-awg10', 'route', noOutbounds, awgOptions);
    expect(d).toEqual({ name: 'awg-awg10', label: 'Office VPN', metaSuffix: 'nwg0', kind: 'awg', tone: 'awg' });
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

  it('нормализованное «пресет ИЛИ свои адреса» показывает обе стороны', () => {
    const chips = extractMatcherChips(
      {
        type: 'logical',
        mode: 'or',
        rules: [{ rule_set: ['geosite-discord'] }, { ip_cidr: ['66.22.192.0/18'] }],
        action: 'route',
        outbound: 'vpn',
      },
      noRulesets,
    );
    expect(chips).toEqual([
      { kind: 'ip', label: '66.22.192.0/18', mono: true },
      {
        kind: 'ruleset',
        label: 'geosite-discord',
        rulesetTag: 'geosite-discord',
        rulesetType: undefined,
      },
    ]);
  });

  it('source_ip_cidr → src chips with mono', () => {
    const chips = extractMatcherChips({ source_ip_cidr: ['192.168.1.0/24'] }, noRulesets);
    expect(chips).toEqual([{ kind: 'src', label: '192.168.1.0/24', mono: true }]);
  });

  it('source_mac_address → src chips with mono', () => {
    const chips = extractMatcherChips(
      { source_mac_address: ['aa:bb:cc:dd:ee:ff'], action: 'route', outbound: 'vpn' },
      noRulesets,
    );
    expect(chips).toEqual([{ kind: 'src', label: 'aa:bb:cc:dd:ee:ff', mono: true }]);
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
      { kind: 'ruleset', label: 'Россия (GeoIP)', rulesetTag: 'geoip-ru' },
      { kind: 'ruleset', label: 'unknown-set', rulesetTag: 'unknown-set' },
    ]);
  });

  it('rule_set chips include rulesetType from config ruleSets', () => {
    const chips = extractMatcherChips(
      { rule_set: ['inline-neo-demo', 'geosite-youtube', 'geosite-google'] },
      {},
      [
        { tag: 'inline-neo-demo', type: 'inline' },
        { tag: 'geosite-youtube', type: 'remote', url: 'https://cdn.example.com/youtube.srs' },
        {
          tag: 'geosite-google',
          type: 'remote',
          url: 'http://127.0.0.1:8081/api/singbox/router/rulesets/dat-srs?kind=geosite&tag=GOOGLE',
        },
      ],
    );
    expect(chips.map((c) => c.rulesetType)).toEqual(['inline', 'remote', 'dat']);
  });

  it('network udp → protocol chip UDP', () => {
    const chips = extractMatcherChips({ network: 'udp', rule_set: ['geoip-google'] }, noRulesets);
    expect(chips).toContainEqual({ kind: 'protocol', label: 'UDP' });
    expect(chips).toContainEqual({
      kind: 'ruleset',
      label: 'geoip-google',
      rulesetTag: 'geoip-google',
      rulesetType: undefined,
    });
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

describe('firstDomainFromInlineRules', () => {
  it('returns first domain_suffix', () => {
    expect(
      firstDomainFromInlineRules([{ domain_suffix: ['youtube.com', 'ytimg.com'] }]),
    ).toBe('youtube.com');
  });

  it('falls back to domain field', () => {
    expect(firstDomainFromInlineRules([{ domain: ['exact.example.com'] }])).toBe('exact.example.com');
  });

  it('suffix before domain in later record', () => {
    expect(
      firstDomainFromInlineRules([
        { ip_cidr: ['1.1.1.1/32'] },
        { domain_suffix: ['first.com'] },
      ]),
    ).toBe('first.com');
  });

  it('undefined for empty / missing', () => {
    expect(firstDomainFromInlineRules(undefined)).toBeUndefined();
    expect(firstDomainFromInlineRules([])).toBeUndefined();
    expect(firstDomainFromInlineRules([{ ip_cidr: ['10.0.0.0/8'] }])).toBeUndefined();
  });

  it('trims whitespace', () => {
    expect(firstDomainFromInlineRules([{ domain_suffix: ['  spaced.com  '] }])).toBe('spaced.com');
  });
});

describe('extractMatchersForCard', () => {
  const ruleSets = [
    { tag: 'custom-1', type: 'inline' as const, rules: [{ domain_suffix: ['a.com', 'b.com'] }] },
    { tag: 'neo-demo', type: 'inline' as const, rules: [{ domain_suffix: ['hidden.com'] }] },
  ];

  it('expands custom-N inline to domain chips', () => {
    const rule = { rule_set: ['custom-1'], outbound: 'warp' };
    const sim = classifyRuleSimplicity(rule, ruleSets);
    const chips = extractMatchersForCard(rule, sim, {}, ruleSets);
    expect(chips).toEqual([
      { kind: 'domain', label: 'a.com' },
      { kind: 'domain', label: 'b.com' },
    ]);
  });

  it('named inline shows ruleset chip', () => {
    const rule = { rule_set: ['neo-demo'], outbound: 'warp' };
    const sim = classifyRuleSimplicity(rule, ruleSets);
    const chips = extractMatchersForCard(rule, sim, { 'neo-demo': 'neo-demo' }, ruleSets);
    expect(chips).toHaveLength(1);
    expect(chips[0].kind).toBe('ruleset');
    expect(chips[0].label).toBe('neo-demo');
  });

  it('inline-text shows domain and ip chips', () => {
    const rule = { domain_suffix: ['x.com'], ip_cidr: ['10.0.0.0/8'], outbound: 'direct' };
    const sim = classifyRuleSimplicity(rule, ruleSets);
    const chips = extractMatchersForCard(rule, sim, {}, ruleSets);
    expect(chips).toEqual([
      { kind: 'domain', label: 'x.com' },
      { kind: 'ip', label: '10.0.0.0/8', mono: true },
    ]);
  });

  it('custom-N: только IP в наборе', () => {
    const rs = [{ tag: 'custom-2', type: 'inline' as const, rules: [{ ip_cidr: ['1.1.1.1/32'] }] }];
    const rule = { rule_set: ['custom-2'], outbound: 'warp' };
    const sim = classifyRuleSimplicity(rule, rs);
    const chips = extractMatchersForCard(rule, sim, {}, rs);
    expect(chips).toEqual([{ kind: 'ip', label: '1.1.1.1', mono: true }]);
  });

  it('custom-N: пустой набор → ruleset chip', () => {
    const rs = [{ tag: 'custom-3', type: 'inline' as const, rules: [] }];
    const rule = { rule_set: ['custom-3'], outbound: 'warp' };
    const sim = classifyRuleSimplicity(rule, rs);
    const chips = extractMatchersForCard(rule, sim, { 'custom-3': 'custom-3' }, rs);
    expect(chips).toHaveLength(1);
    expect(chips[0].kind).toBe('ruleset');
  });

  it('external simple → ruleset chip', () => {
    const rs = [
      { tag: 'geosite-discord', type: 'remote' as const, url: 'https://x/discord.srs' },
    ];
    const rule = { rule_set: ['geosite-discord'], outbound: 'warp' };
    const sim = classifyRuleSimplicity(rule, rs);
    const chips = extractMatchersForCard(rule, sim, {}, rs);
    expect(chips[0].kind).toBe('ruleset');
    expect(chips[0].rulesetType).toBe('remote');
  });

  it('complex → все матчеры rule', () => {
    const rule = { domain_suffix: ['x.com'], port: [443], outbound: 'warp' };
    const sim = classifyRuleSimplicity(rule, ruleSets);
    expect(sim.simple).toBe(false);
    const chips = extractMatchersForCard(rule, sim, {}, ruleSets);
    expect(chips.some((c) => c.kind === 'port')).toBe(true);
  });
});

describe('singboxRuleToCard', () => {
  it('produces a netflix card', () => {
    const card = singboxRuleToCard(
      { domain_suffix: ['netflix.com'], outbound: 'warp' },
      0,
      [{ tag: 'warp', type: 'wireguard' } as unknown as SingboxRouterOutbound],
      {},
      [],
      [],
      catalog,
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
    expect(card.outbound.label).toBe('Office VPN');
    expect(card.outbound.metaSuffix).toBe('nwg0');
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

  it('uses uiKey when provided', () => {
    const r: SingboxRouterRule = { domain_suffix: ['netflix.com'], outbound: 'warp' };
    const card = singboxRuleToCard(r, 0, [], {}, undefined, [], [], [], null, [], [], 'ui-key-1');
    expect(card.id).toBe('ui-key-1');
  });

  it('hides -srs companion suffix on ruleset chip during materialization', () => {
    const card = singboxRuleToCard(
      { rule_set: ['geosite-samsung-srs'], outbound: 'direct' },
      0,
      [],
      {},
      [],
      [],
      [],
      [{ tag: 'geosite-samsung', type: 'inline', rules: [{ domain_suffix: ['samsung.com'] }] }],
    );
    const rsChip = card.matchers.find((m) => m.kind === 'ruleset');
    expect(rsChip?.label).toBe('geosite-samsung');
    expect(rsChip?.rulesetTag).toBe('geosite-samsung');
  });

  it('title never shows -srs companion suffix', () => {
    const card = singboxRuleToCard(
      { rule_set: ['geosite-samsung-srs'], outbound: 'direct' },
      0,
      [],
      {},
      [],
      [],
      [],
      [{ tag: 'geosite-samsung', type: 'inline', rules: [{ domain_suffix: ['samsung.com'] }] }],
    );
    expect(card.title).not.toContain('-srs');
  });

  it('sniff system rule — title/subtitle по дизайну', () => {
    const card = singboxRuleToCard({ action: 'sniff' }, 0, [], {});
    expect(card.isSystem).toBe(true);
    expect(card.title).toBe('Анализ протокола');
    expect(card.subtitle).toBe('sniff');
    expect(card.action).toBe('sniff');
    expect(card.outbound.kind).toBe('sniff');
    expect(card.outbound.label).toBe('SNIFF');
    expect(card.tooltip).toMatch(/SNI/);
  });

  it('hijack-dns system rule — title/subtitle по дизайну', () => {
    const card = singboxRuleToCard({ action: 'hijack-dns' }, 0, [], {});
    expect(card.isSystem).toBe(true);
    expect(card.title).toBe('Перехват DNS');
    expect(card.subtitle).toBe('protocol=dns OR port=53');
    expect(card.action).toBe('hijack-dns');
    expect(card.outbound.kind).toBe('hijack-dns');
    expect(card.outbound.label).toBe('HIJACK-DNS');
    expect(card.tooltip).toMatch(/DNS/);
  });

  it('ip_is_private bypass — subtitle с RFC1918', () => {
    const card = singboxRuleToCard({ ip_is_private: true, outbound: 'direct' }, 0, [], {});
    expect(card.title).toBe('Локальная сеть');
    expect(card.subtitle).toBe('RFC1918 · loopback · link-local · CGNAT');
    expect(card.tooltip).toMatch(/LAN/);
  });

  it('route-options UDP-timeout system rule — не как route/direct, а UDP TIMEOUT', () => {
    const card = singboxRuleToCard(
      { action: 'route-options', network: 'udp', udp_timeout: '1h' },
      3,
      [],
      {},
    );
    expect(card.isSystem).toBe(true);
    expect(card.action).toBe('udp-timeout');
    expect(card.title).toBe('UDP таймаут');
    expect(card.subtitle).toBe('udp · timeout=1h');
    expect(card.outbound.kind).toBe('udp-timeout');
    expect(card.outbound.label).toBe('UDP TIMEOUT');
    // НЕ должно выглядеть как обычный route в direct
    expect(card.outbound.kind).not.toBe('direct');
    expect(card.tooltip).toMatch(/таймаут/i);
    expect(card.tooltip).toContain('1h');
  });

  it('custom-N inline uses first domain as title and catalog icon', () => {
    const card = singboxRuleToCard(
      { rule_set: ['custom-1'], outbound: 'warp' },
      0,
      [{ tag: 'warp', type: 'wireguard' } as unknown as SingboxRouterOutbound],
      {},
      [],
      [],
      catalog,
      [{ tag: 'custom-1', type: 'inline', rules: [{ domain_suffix: ['netflix.com', 'nflxext.com'] }] }],
    );
    expect(card.title).toBe('netflix.com');
    expect(card.serviceKey).toBe('netflix');
    expect(card.matchers).toHaveLength(2);
    expect(card.simplicity).toEqual({
      simple: true,
      kind: 'inline-set',
      inlineRuleSetTag: 'custom-1',
    });
  });

  it('custom-N без домена: title = tag, icon custom', () => {
    const card = singboxRuleToCard(
      { rule_set: ['custom-1'], outbound: 'warp' },
      0,
      [],
      {},
      [],
      [],
      [],
      [{ tag: 'custom-1', type: 'inline', rules: [{ ip_cidr: ['10.0.0.0/8'] }] }],
    );
    expect(card.title).toBe('custom-1');
    expect(card.serviceKey).toBe('custom');
    expect(card.matchers[0].kind).toBe('ip');
  });

  it('custom-N неизвестный домен: title домен, icon custom', () => {
    const card = singboxRuleToCard(
      { rule_set: ['custom-1'], outbound: 'direct' },
      0,
      [],
      {},
      [],
      [],
      catalog,
      [{ tag: 'custom-1', type: 'inline', rules: [{ domain_suffix: ['unknown-service.xyz'] }] }],
    );
    expect(card.title).toBe('unknown-service.xyz');
    expect(card.serviceKey).toBe('custom');
  });

  it('inline-text: simplicity + matchers', () => {
    const card = singboxRuleToCard(
      { domain_suffix: ['foo.com'], ip_cidr: ['192.168.0.0/16'], outbound: 'direct' },
      0,
      [],
      {},
    );
    expect(card.simplicity).toEqual({ simple: true, kind: 'inline-text' });
    expect(card.matchers).toHaveLength(2);
    expect(card.title).toBe('foo.com');
  });

  it('complex rule: simplicity false', () => {
    const card = singboxRuleToCard(
      { domain_suffix: ['foo.com'], port: [443], outbound: 'warp' },
      0,
      [],
      {},
    );
    expect(card.simplicity).toEqual({ simple: false });
  });

  it('external simple: simplicity + preset title', () => {
    const presets = [{
      id: 'discord',
      name: 'Discord',
      iconSlug: 'discord',
      ruleSets: [{ tag: 'geosite-discord', url: 'https://x/discord.srs' }],
      rules: [{ ruleSetRef: 'geosite-discord', actionTarget: 'tunnel' as const }],
    }];
    const card = singboxRuleToCard(
      { rule_set: ['geosite-discord'], outbound: 'warp' },
      0,
      [],
      {},
      presets,
      [],
      catalog,
      [{ tag: 'geosite-discord', type: 'remote', url: 'https://x/discord.srs' }],
    );
    expect(card.simplicity).toEqual({
      simple: true,
      kind: 'external',
      externalRuleSetTag: 'geosite-discord',
    });
    expect(card.serviceKey).toBe('discord');
    expect(card.title).toBe('Discord');
  });

  it('subtitle при >4 матчерах', () => {
    const domains = ['a.com', 'b.com', 'c.com', 'd.com', 'e.com'];
    const card = singboxRuleToCard(
      { domain_suffix: domains, outbound: 'direct' },
      0,
      [],
      {},
    );
    expect(card.subtitle).toBe('5 матчеров');
  });

  it('named inline-set: title от preset ruleset, не первый домен', () => {
    const card = singboxRuleToCard(
      { rule_set: ['neo-demo'], outbound: 'warp' },
      0,
      [],
      {},
      [],
      [],
      catalog,
      [{ tag: 'neo-demo', type: 'inline', rules: [{ domain_suffix: ['hidden.com'] }] }],
    );
    expect(card.title).toBe('neo-demo');
    expect(card.matchers[0].kind).toBe('ruleset');
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
