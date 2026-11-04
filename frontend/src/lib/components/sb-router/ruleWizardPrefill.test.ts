import { describe, it, expect } from 'vitest';
import { prefillWizardFromRule } from './ruleWizardPrefill';
import type { SingboxRouterPreset, SingboxRouterRule, SingboxRouterRuleSet } from '$lib/types';

const presets: SingboxRouterPreset[] = [
  {
    id: 'netflix',
    name: 'Netflix',
    ruleSets: [{ tag: 'geosite-netflix', url: 'https://example.com/netflix.srs' }],
    rules: [{ ruleSetRef: 'geosite-netflix', actionTarget: 'tunnel' }],
  },
  {
    id: 'discord',
    name: 'Discord',
    ruleSets: [{ tag: 'geosite-discord', url: 'https://example.com/discord.srs' }],
    rules: [{ ruleSetRef: 'geosite-discord', actionTarget: 'tunnel' }],
  },
];

const remoteDiscord: SingboxRouterRuleSet = {
  tag: 'geosite-discord',
  type: 'remote',
  url: 'https://x/discord.srs',
};

describe('prefillWizardFromRule — external', () => {
  it('svc template + tunnel', () => {
    const rule: SingboxRouterRule = {
      rule_set: ['geosite-discord'],
      action: 'route',
      outbound: 'warp',
    };
    const r = prefillWizardFromRule(rule, presets, [remoteDiscord]);
    expect(r).toMatchObject({
      editMode: 'external',
      templateIds: ['svc:discord'],
      rulesList: '',
      outboundCategory: 'tunnel',
      tunnelTags: ['warp'],
    });
    expect(r.wasInlineText).toBeUndefined();
  });

  it('rs: fallback без preset', () => {
    const rule: SingboxRouterRule = { rule_set: ['geosite-unknown'], action: 'route', outbound: 'warp' };
    const r = prefillWizardFromRule(rule, [], [
      { tag: 'geosite-unknown', type: 'remote', url: 'https://x/x.srs' },
    ]);
    expect(r.templateIds).toEqual(['rs:geosite-unknown']);
    expect(r.editMode).toBe('external');
  });

  it('reject → block category', () => {
    const rule: SingboxRouterRule = { rule_set: ['geosite-discord'], action: 'reject' };
    const r = prefillWizardFromRule(rule, presets, [remoteDiscord]);
    expect(r.outboundCategory).toBe('block');
    expect(r.tunnelTags).toEqual([]);
  });

  it('direct outbound', () => {
    const rule: SingboxRouterRule = {
      rule_set: ['geosite-discord'],
      action: 'route',
      outbound: 'direct',
    };
    const r = prefillWizardFromRule(rule, presets, [remoteDiscord]);
    expect(r.outboundCategory).toBe('direct');
    expect(r.tunnelTags).toEqual([]);
  });

  it('composite outbound → раскрывает участников', () => {
    const rule: SingboxRouterRule = {
      rule_set: ['geosite-discord'],
      action: 'route',
      outbound: 'custom-composite-1',
    };
    const r = prefillWizardFromRule(rule, presets, [remoteDiscord], [
      { type: 'selector', tag: 'custom-composite-1', outbounds: ['warp', 'awg10'] },
    ]);
    expect(r.outboundCategory).toBe('tunnel');
    expect(r.tunnelTags).toEqual(['warp', 'awg10']);
  });

  it('подписка остаётся одним выходом', () => {
    const rule: SingboxRouterRule = {
      rule_set: ['geosite-discord'],
      action: 'route',
      outbound: 'sub-40be7ea6',
    };
    const r = prefillWizardFromRule(rule, presets, [remoteDiscord], [
      {
        type: 'urltest',
        tag: 'sub-40be7ea6',
        source: 'subscription',
        outbounds: ['sub-40be7ea6-a', 'sub-40be7ea6-b'],
      },
    ]);
    expect(r.tunnelTags).toEqual(['sub-40be7ea6']);
    expect(r.compositeMode).toBe('urltest');
  });
});

describe('prefillWizardFromRule — inline', () => {
  it('inline-text: list + wasInlineText', () => {
    const rule: SingboxRouterRule = {
      domain_suffix: ['example.com'],
      ip_cidr: ['10.0.0.0/8'],
      action: 'route',
      outbound: 'direct',
    };
    const r = prefillWizardFromRule(rule, [], []);
    expect(r.editMode).toBe('inline');
    expect(r.templateIds).toEqual([]);
    expect(r.rulesList).toContain('example.com');
    expect(r.rulesList).toContain('10.0.0.0/8');
    expect(r.rulesList).not.toContain('port:');
    expect(r.wasInlineText).toBe(true);
    expect(r.existingInlineRuleSetTag).toBeUndefined();
  });

  it('custom inline-set: list + existing tag', () => {
    const rule: SingboxRouterRule = { rule_set: ['custom-1'], action: 'route', outbound: 'direct' };
    const ruleSets: SingboxRouterRuleSet[] = [
      { tag: 'custom-1', type: 'inline', rules: [{ domain_suffix: ['youtube.com'] }] },
    ];
    const r = prefillWizardFromRule(rule, [], ruleSets);
    expect(r.editMode).toBe('inline');
    expect(r.rulesList).toContain('youtube.com');
    expect(r.existingInlineRuleSetTag).toBe('custom-1');
    expect(r.wasInlineText).toBeUndefined();
  });

  it('named inline-set', () => {
    const rule: SingboxRouterRule = { rule_set: ['neo-demo'], action: 'route', outbound: 'direct' };
    const ruleSets: SingboxRouterRuleSet[] = [
      { tag: 'neo-demo', type: 'inline', rules: [{ domain_suffix: ['expert.com'] }] },
    ];
    const r = prefillWizardFromRule(rule, [], ruleSets);
    expect(r.existingInlineRuleSetTag).toBe('neo-demo');
    expect(r.rulesList).toContain('expert.com');
  });

  it('inline-set: пустой ruleset → пустой list', () => {
    const rule: SingboxRouterRule = { rule_set: ['custom-2'], action: 'route', outbound: 'warp' };
    const r = prefillWizardFromRule(rule, [], [
      { tag: 'custom-2', type: 'inline', rules: [] },
    ]);
    expect(r.rulesList).toBe('');
    expect(r.existingInlineRuleSetTag).toBe('custom-2');
  });
});

describe('prefillWizardFromRule — complex / system', () => {
  it('complex: текст + port', () => {
    const rule: SingboxRouterRule = { domain_suffix: ['a.com'], rule_set: ['custom-1'], port: [443] };
    const r = prefillWizardFromRule(rule, [], [
      { tag: 'custom-1', type: 'inline', rules: [] },
    ]);
    expect(r.templateIds).toEqual([]);
    expect(r.rulesList).toBe('');
    expect(r.editMode).toBeUndefined();
  });

  it('system sniff: пустой prefill', () => {
    const r = prefillWizardFromRule({ action: 'sniff' }, presets, []);
    expect(r.rulesList).toBe('');
    expect(r.editMode).toBeUndefined();
  });
});
