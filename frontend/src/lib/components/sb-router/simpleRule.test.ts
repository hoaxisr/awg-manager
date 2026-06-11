import { describe, it, expect } from 'vitest';
import {
  classifyRuleSimplicity,
  COMPLEX_RULE_EDIT_MESSAGE,
  isCustomInlineRuleSetTag,
  singleRuleSetTagFromTemplateId,
  templateIdForExternalRuleSetTag,
} from './simpleRule';
import type { SingboxRouterPreset, SingboxRouterRule, SingboxRouterRuleSet } from '$lib/types';

const presets: SingboxRouterPreset[] = [
  {
    id: 'discord',
    name: 'Discord',
    ruleSets: [{ tag: 'geosite-discord', url: 'https://example.com/discord.srs' }],
    rules: [{ ruleSetRef: 'geosite-discord', actionTarget: 'tunnel' }],
  },
];

const ruleSets: SingboxRouterRuleSet[] = [
  { tag: 'custom-1', type: 'inline', rules: [{ domain_suffix: ['a.com'] }] },
  { tag: 'neo-demo', type: 'inline', rules: [{ domain_suffix: ['b.com'] }] },
  { tag: 'geosite-discord', type: 'remote', url: 'https://example.com/discord.srs' },
  { tag: 'geoip-ru', type: 'local', path: '/data/geoip-ru.srs' },
];

describe('classifyRuleSimplicity — простые', () => {
  it('inline-text: domain_suffix', () => {
    expect(classifyRuleSimplicity({ domain_suffix: ['x.com'], outbound: 'direct' }, ruleSets)).toEqual({
      simple: true,
      kind: 'inline-text',
    });
  });

  it('inline-text: ip_cidr', () => {
    expect(classifyRuleSimplicity({ ip_cidr: ['10.0.0.0/8'] }, ruleSets)).toEqual({
      simple: true,
      kind: 'inline-text',
    });
  });

  it('inline-set: custom-N', () => {
    expect(classifyRuleSimplicity({ rule_set: ['custom-1'], outbound: 'warp' }, ruleSets)).toEqual({
      simple: true,
      kind: 'inline-set',
      inlineRuleSetTag: 'custom-1',
    });
  });

  it('inline-set: именованный', () => {
    expect(classifyRuleSimplicity({ rule_set: ['neo-demo'], outbound: 'warp' }, ruleSets)).toEqual({
      simple: true,
      kind: 'inline-set',
      inlineRuleSetTag: 'neo-demo',
    });
  });

  it('external: remote srs', () => {
    expect(classifyRuleSimplicity({ rule_set: ['geosite-discord'], outbound: 'warp' }, ruleSets)).toEqual({
      simple: true,
      kind: 'external',
      externalRuleSetTag: 'geosite-discord',
    });
  });

  it('external: local srs', () => {
    expect(classifyRuleSimplicity({ rule_set: ['geoip-ru'], outbound: 'direct' }, ruleSets)).toEqual({
      simple: true,
      kind: 'external',
      externalRuleSetTag: 'geoip-ru',
    });
  });
});

describe('classifyRuleSimplicity — сложные', () => {
  it('системные: sniff', () => {
    expect(classifyRuleSimplicity({ action: 'sniff' }, ruleSets)).toEqual({ simple: false });
  });

  it('системные: hijack-dns', () => {
    expect(classifyRuleSimplicity({ action: 'hijack-dns' }, ruleSets)).toEqual({ simple: false });
  });

  it('системные: ip_is_private + direct', () => {
    expect(classifyRuleSimplicity({ ip_is_private: true, outbound: 'direct' }, ruleSets)).toEqual({
      simple: false,
    });
  });

  it('port на rule', () => {
    expect(classifyRuleSimplicity({ domain_suffix: ['x.com'], port: [443] }, ruleSets).simple).toBe(false);
  });

  it('source_ip_cidr на rule', () => {
    expect(
      classifyRuleSimplicity({ domain_suffix: ['x.com'], source_ip_cidr: ['1.1.1.1'] }, ruleSets).simple,
    ).toBe(false);
  });

  it('domain_keyword на rule (неизвестное поле)', () => {
    expect(
      classifyRuleSimplicity({ domain_keyword: ['ads'] } as SingboxRouterRule, ruleSets).simple,
    ).toBe(false);
  });

  it('domain_regex на rule (неизвестное поле)', () => {
    expect(
      classifyRuleSimplicity({ domain_regex: ['.*\\.ads\\.'] } as SingboxRouterRule, ruleSets).simple,
    ).toBe(false);
  });

  it('domain (exact) на rule (неизвестное поле)', () => {
    expect(
      classifyRuleSimplicity({ domain: ['exact.host'] } as SingboxRouterRule, ruleSets).simple,
    ).toBe(false);
  });

  it('protocol на rule', () => {
    expect(classifyRuleSimplicity({ protocol: 'quic', outbound: 'direct' }, ruleSets).simple).toBe(false);
  });

  it('ip_is_private с не-direct outbound', () => {
    expect(
      classifyRuleSimplicity({ ip_is_private: true, domain_suffix: ['x.com'], outbound: 'warp' }, ruleSets)
        .simple,
    ).toBe(false);
  });

  it('logical rule (type/mode/rules)', () => {
    expect(
      classifyRuleSimplicity(
        { type: 'logical', mode: 'or', rules: [{ domain_suffix: ['x.com'] }], outbound: 'warp' } as SingboxRouterRule,
        ruleSets,
      ).simple,
    ).toBe(false);
  });

  it('текст + rule_set', () => {
    expect(
      classifyRuleSimplicity({ domain_suffix: ['x.com'], rule_set: ['custom-1'] }, ruleSets).simple,
    ).toBe(false);
  });

  it('два rule_set', () => {
    expect(classifyRuleSimplicity({ rule_set: ['custom-1', 'geosite-discord'] }, ruleSets).simple).toBe(false);
  });

  it('пустое правило', () => {
    expect(classifyRuleSimplicity({ outbound: 'direct' }, ruleSets).simple).toBe(false);
  });

  it('rule_set без записи в config', () => {
    expect(classifyRuleSimplicity({ rule_set: ['missing-set'] }, ruleSets).simple).toBe(false);
  });

  it('только rule_set без известного типа (orphan tag в map)', () => {
    expect(classifyRuleSimplicity({ rule_set: ['orphan'] }, []).simple).toBe(false);
  });
});

describe('isCustomInlineRuleSetTag', () => {
  it.each([
    ['custom-1', true],
    ['custom-42', true],
    ['CUSTOM-3', true],
    ['custom', false],
    ['custom-', false],
    ['custom-x', false],
    ['neo-demo', false],
    ['my-custom-1', false],
  ])('%s → %s', (tag, expected) => {
    expect(isCustomInlineRuleSetTag(tag)).toBe(expected);
  });
});

describe('template helpers', () => {
  it('templateIdForExternalRuleSetTag: preset match', () => {
    expect(templateIdForExternalRuleSetTag('geosite-discord', presets)).toBe('svc:discord');
  });

  it('templateIdForExternalRuleSetTag: fallback rs:', () => {
    expect(templateIdForExternalRuleSetTag('geosite-ads', [])).toBe('rs:geosite-ads');
  });

  it('singleRuleSetTagFromTemplateId: svc:', () => {
    expect(singleRuleSetTagFromTemplateId('svc:discord', presets)).toBe('geosite-discord');
  });

  it('singleRuleSetTagFromTemplateId: rs:', () => {
    expect(singleRuleSetTagFromTemplateId('rs:foo', presets)).toBe('foo');
  });

  it('singleRuleSetTagFromTemplateId: unknown → undefined', () => {
    expect(singleRuleSetTagFromTemplateId('bad:id', presets)).toBeUndefined();
    expect(singleRuleSetTagFromTemplateId('svc:missing', presets)).toBeUndefined();
  });
});

describe('COMPLEX_RULE_EDIT_MESSAGE', () => {
  it('непустая строка для UI', () => {
    expect(COMPLEX_RULE_EDIT_MESSAGE).toMatch(/экспертном режиме/i);
  });
});
