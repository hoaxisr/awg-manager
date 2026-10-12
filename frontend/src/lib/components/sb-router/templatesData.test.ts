import { describe, it, expect } from 'vitest';
import {
  buildTemplateList,
  filterByCategory,
  countByCategory,
  type TemplateGroup,
} from './templatesData';
import type { SingboxRouterPreset, SingboxRouterRuleSet } from '$lib/types';

const presetNetflix: SingboxRouterPreset = {
  id: 'netflix', name: 'Netflix', category: 'media',
  ruleSets: [], rules: [],
};
const presetYoutube: SingboxRouterPreset = {
  id: 'youtube', name: 'YouTube', category: 'media',
  ruleSets: [], rules: [],
};
const rsTelegram: SingboxRouterRuleSet = { tag: 'telegram', type: 'remote' };
const rsGeoipRu: SingboxRouterRuleSet = { tag: 'geoip:ru', type: 'inline' };

describe('templatesData.buildTemplateList', () => {
  it('without rulesets returns only services group', () => {
    const groups = buildTemplateList([presetNetflix], [], '');
    expect(groups).toHaveLength(1);
    expect(groups[0].category).toBe('services');
    expect(groups[0].items).toHaveLength(1);
    expect(groups[0].items[0].id).toBe('svc:netflix');
  });

  it('with rulesets returns both groups', () => {
    const groups = buildTemplateList([presetNetflix], [rsTelegram], '');
    expect(groups.map((g) => g.category)).toEqual(['services', 'rulesets']);
    expect(groups[1].items[0].id).toBe('rs:telegram');
  });

  it('filters services by name substring (case insensitive)', () => {
    const groups = buildTemplateList([presetNetflix, presetYoutube], [], 'netf');
    expect(groups[0].items.map((i) => (i as { presetId: string }).presetId)).toEqual(['netflix']);
  });

  it('filters rulesets by tag substring', () => {
    const groups = buildTemplateList([], [rsTelegram, rsGeoipRu], 'geoip');
    const rsGroup = groups.find((g) => g.category === 'rulesets');
    expect(rsGroup?.items.map((i) => (i as { tag: string }).tag)).toEqual(['geoip:ru']);
  });

  it('returns empty groups omitted when no matches', () => {
    const groups = buildTemplateList([presetNetflix], [rsTelegram], 'zzzzzz');
    expect(groups).toHaveLength(0);
  });
});

describe('templatesData.filterByCategory', () => {
  const groups: TemplateGroup[] = [
    { category: 'services', items: [{ id: 'svc:x', category: 'services', presetId: 'x', name: 'X' }] },
    { category: 'rulesets', items: [{ id: 'rs:y', category: 'rulesets', tag: 'y', type: 'remote' }] },
  ];

  it('returns all groups when filter=all', () => {
    expect(filterByCategory(groups, 'all')).toEqual(groups);
  });

  it('returns only services when filter=services', () => {
    const r = filterByCategory(groups, 'services');
    expect(r).toHaveLength(1);
    expect(r[0].category).toBe('services');
  });

  it('returns only rulesets when filter=rulesets', () => {
    const r = filterByCategory(groups, 'rulesets');
    expect(r).toHaveLength(1);
    expect(r[0].category).toBe('rulesets');
  });
});

describe('templatesData.countByCategory', () => {
  it('counts items per category + total', () => {
    const groups = buildTemplateList([presetNetflix, presetYoutube], [rsTelegram], '');
    const c = countByCategory(groups);
    expect(c.services).toBe(2);
    expect(c.rulesets).toBe(1);
    expect(c.all).toBe(3);
  });

  it('returns 0 for absent categories', () => {
    const groups = buildTemplateList([presetNetflix], [], '');
    const c = countByCategory(groups);
    expect(c.services).toBe(1);
    expect(c.rulesets).toBe(0);
    expect(c.all).toBe(1);
  });
});
