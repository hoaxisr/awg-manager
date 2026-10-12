import { describe, it, expect, vi, beforeEach } from 'vitest';

vi.mock('$lib/api/client', () => ({
  api: {
    singboxRouterApplyPreset: vi.fn(),
    singboxRouterAddRule: vi.fn(),
    singboxRouterAddRuleSet: vi.fn(),
    singboxRouterAddOutbound: vi.fn(),
    singboxRouterUpdateRule: vi.fn(),
    singboxRouterUpdateRuleSet: vi.fn(),
    expandGeoTag: vi.fn(),
  },
}));

vi.mock('$lib/utils/singboxInlineGeoExpand', () => ({
  expandGeoLinesInInput: vi.fn(async (text: string) => ({ text, warnings: [] })),
}));

import { api } from '$lib/api/client';
import { expandGeoLinesInInput } from '$lib/utils/singboxInlineGeoExpand';
import {
  resolveOutbound, submitWizard, submitWizardEdit, ValidationError,
  nextCustomRuleSetTag, parseCustomList, resolveTunnelOutbound,
} from './addWizardActions';
import type { CustomMatcherFields } from './addWizardStore';
import type { TemplateGroup } from './templatesData';
import type { SingboxRouterPreset } from '$lib/types';

const emptyCustom: CustomMatcherFields = { rulesList: '' };
const groups: TemplateGroup[] = [
  {
    category: 'services',
    items: [{ id: 'svc:netflix', category: 'services', presetId: 'netflix', name: 'Netflix' }],
  },
  {
    category: 'rulesets',
    items: [{ id: 'rs:geoip-ru', category: 'rulesets', tag: 'geoip-ru', type: 'local' }],
  },
];

const presets: SingboxRouterPreset[] = [
  {
    id: 'netflix',
    name: 'Netflix',
    ruleSets: [{ tag: 'geosite-netflix', url: 'https://example.com/netflix.srs' }],
    rules: [{ ruleSetRef: 'geosite-netflix', actionTarget: 'tunnel' }],
  },
];

const noOutbounds: never[] = [];

describe('resolveOutbound', () => {
  it('tunnel returns single tag', async () => {
    await expect(resolveOutbound('tunnel', ['warp'], noOutbounds)).resolves.toBe('warp');
  });

  it('tunnel without tags throws', async () => {
    await expect(resolveOutbound('tunnel', [], noOutbounds)).rejects.toThrow(ValidationError);
  });

  it('direct returns "direct"', async () => {
    await expect(resolveOutbound('direct', [], noOutbounds)).resolves.toBe('direct');
  });

  it('block returns "block"', async () => {
    await expect(resolveOutbound('block', [], noOutbounds)).resolves.toBe('block');
  });
});

describe('resolveTunnelOutbound', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('один туннель — без API', async () => {
    await expect(resolveTunnelOutbound(['warp'], [])).resolves.toBe('warp');
    expect(api.singboxRouterAddOutbound).not.toHaveBeenCalled();
  });

  it('находит существующий composite', async () => {
    const tag = await resolveTunnelOutbound(['warp', 'awg10'], [
      { type: 'selector', tag: 'custom-composite-1', outbounds: ['warp', 'awg10'] },
    ]);
    expect(tag).toBe('custom-composite-1');
    expect(api.singboxRouterAddOutbound).not.toHaveBeenCalled();
  });

  it('создаёт custom-composite-N при отсутствии совпадения', async () => {
    (api.singboxRouterAddOutbound as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    const tag = await resolveTunnelOutbound(['warp', 'awg20'], [
      { type: 'selector', tag: 'custom-composite-1', outbounds: ['warp', 'awg10'] },
    ]);
    expect(tag).toBe('custom-composite-2');
    expect(api.singboxRouterAddOutbound).toHaveBeenCalledWith({
      type: 'urltest',
      tag: 'custom-composite-2',
      outbounds: ['warp', 'awg20'],
      url: 'https://www.gstatic.com/generate_204',
      interval: '60s',
      tolerance: 50,
    });
  });
});

describe('nextCustomRuleSetTag', () => {
  it('первый свободный custom-N', () => {
    expect(nextCustomRuleSetTag([])).toBe('custom-1');
    expect(nextCustomRuleSetTag(['custom-1', 'geosite-x'])).toBe('custom-2');
    expect(nextCustomRuleSetTag(['custom-2'])).toBe('custom-1');
  });
});

describe('submitWizard', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    (expandGeoLinesInInput as ReturnType<typeof vi.fn>).mockImplementation(
      async (text: string) => ({ text, warnings: [] }),
    );
  });

  it('no templates AND no custom → throws ValidationError', async () => {
    await expect(submitWizard({
      selectedTemplates: [], customFields: emptyCustom,
      outboundCategory: 'tunnel', tunnelTags: ['warp'], groups,
      existingRuleSetTags: [], existingOutbounds: [],
    })).rejects.toThrow(ValidationError);
  });

  it('templates only → submitTemplates called', async () => {
    (api.singboxRouterApplyPreset as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    const r = await submitWizard({
      selectedTemplates: ['svc:netflix'], customFields: emptyCustom,
      outboundCategory: 'tunnel', tunnelTags: ['warp'], groups,
      existingRuleSetTags: [], existingOutbounds: [],
    });
    expect(api.singboxRouterApplyPreset).toHaveBeenCalledWith('netflix', 'warp');
    expect(r.successes).toEqual(['svc:netflix']);
  });

  it('custom only → rule_set создаётся и ссылается rule', async () => {
    (api.singboxRouterAddRuleSet as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    (api.singboxRouterAddRule as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    const r = await submitWizard({
      selectedTemplates: [],
      customFields: { rulesList: 'domain:example.com' },
      outboundCategory: 'tunnel', tunnelTags: ['warp'], groups,
      existingRuleSetTags: [], existingOutbounds: [],
    });
    expect(api.singboxRouterAddRuleSet).toHaveBeenCalledWith(
      expect.objectContaining({ tag: 'custom-1', type: 'inline' }),
    );
    expect(api.singboxRouterAddRule).toHaveBeenCalledWith(
      expect.objectContaining({ rule_set: ['custom-1'], outbound: 'warp', action: 'route' }),
    );
    expect(r.successes).toEqual(['custom']);
  });

  it('несколько туннелей → composite outbound + rule', async () => {
    (api.singboxRouterAddOutbound as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    (api.singboxRouterAddRuleSet as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    (api.singboxRouterAddRule as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    await submitWizard({
      selectedTemplates: [],
      customFields: { rulesList: 'domain:example.com' },
      outboundCategory: 'tunnel', tunnelTags: ['warp', 'awg10'], groups,
      existingRuleSetTags: [], existingOutbounds: [],
    });
    expect(api.singboxRouterAddOutbound).toHaveBeenCalledWith(
      expect.objectContaining({ tag: 'custom-composite-1', outbounds: ['warp', 'awg10'] }),
    );
    expect(api.singboxRouterAddRule).toHaveBeenCalledWith(
      expect.objectContaining({ outbound: 'custom-composite-1' }),
    );
  });

  it('block outbound → action=reject в rule', async () => {
    (api.singboxRouterAddRuleSet as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    (api.singboxRouterAddRule as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    const r = await submitWizard({
      selectedTemplates: [],
      customFields: { rulesList: 'domain:example.com' },
      outboundCategory: 'block', tunnelTags: [], groups,
      existingRuleSetTags: [], existingOutbounds: [],
    });
    expect(api.singboxRouterAddRule).toHaveBeenCalledWith(
      expect.objectContaining({ rule_set: ['custom-1'], action: 'reject' }),
    );
    expect(r.successes).toEqual(['custom']);
  });

  it('existingRuleSetTags → следующий свободный тег', async () => {
    (api.singboxRouterAddRuleSet as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    (api.singboxRouterAddRule as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    await submitWizard({
      selectedTemplates: [],
      customFields: { rulesList: 'domain:example.com' },
      outboundCategory: 'direct', tunnelTags: [], groups,
      existingRuleSetTags: ['custom-1', 'custom-2'], existingOutbounds: [],
    });
    expect(api.singboxRouterAddRuleSet).toHaveBeenCalledWith(
      expect.objectContaining({ tag: 'custom-3' }),
    );
  });

  it('templates + custom → both submitted, aggregated', async () => {
    (api.singboxRouterApplyPreset as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    (api.singboxRouterAddRuleSet as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    (api.singboxRouterAddRule as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    const r = await submitWizard({
      selectedTemplates: ['svc:netflix'],
      customFields: { rulesList: 'domain:example.com' },
      outboundCategory: 'tunnel', tunnelTags: ['warp'], groups,
      existingRuleSetTags: [], existingOutbounds: [],
    });
    expect(r.successes.sort()).toEqual(['custom', 'svc:netflix']);
    expect(r.failures).toEqual([]);
  });

  it('partial failure: custom API fails', async () => {
    (api.singboxRouterApplyPreset as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    (api.singboxRouterAddRuleSet as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('bad rule'));
    const r = await submitWizard({
      selectedTemplates: ['svc:netflix'],
      customFields: { rulesList: 'domain:example.com' },
      outboundCategory: 'tunnel', tunnelTags: ['warp'], groups,
      existingRuleSetTags: [], existingOutbounds: [],
    });
    expect(r.successes).toEqual(['svc:netflix']);
    expect(r.failures).toEqual([{ id: 'custom', error: 'bad rule' }]);
  });

  it('tunnel category без tunnelTags → throws', async () => {
    await expect(submitWizard({
      selectedTemplates: ['svc:netflix'], customFields: emptyCustom,
      outboundCategory: 'tunnel', tunnelTags: [], groups,
      existingRuleSetTags: [], existingOutbounds: [],
    })).rejects.toThrow(ValidationError);
  });
});

describe('submitWizardEdit', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    (expandGeoLinesInInput as ReturnType<typeof vi.fn>).mockImplementation(
      async (text: string) => ({ text, warnings: [] }),
    );
  });

  const baseInlineArgs = {
    ruleIndex: 3,
    editMode: 'inline' as const,
    selectedTemplates: [] as string[],
    customFields: { rulesList: 'new.example.com' },
    outboundCategory: 'tunnel' as const,
    tunnelTags: ['warp'],
    groups,
    presets,
    existingRuleSetTags: ['custom-1', 'geosite-netflix'],
    existingOutbounds: [],
  };

  it('external: svc template → update rule', async () => {
    (api.singboxRouterUpdateRule as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    await submitWizardEdit({
      ruleIndex: 1,
      editMode: 'external',
      selectedTemplates: ['svc:netflix'],
      customFields: emptyCustom,
      outboundCategory: 'tunnel',
      tunnelTags: ['warp'],
      groups,
      presets,
      existingRuleSetTags: [],
      existingOutbounds: [],
    });
    expect(api.singboxRouterUpdateRule).toHaveBeenCalledWith(1, {
      rule_set: ['geosite-netflix'],
      action: 'route',
      outbound: 'warp',
    });
    expect(api.singboxRouterAddRuleSet).not.toHaveBeenCalled();
  });

  it('external: rs template → update rule', async () => {
    (api.singboxRouterUpdateRule as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    await submitWizardEdit({
      ruleIndex: 2,
      editMode: 'external',
      selectedTemplates: ['rs:geoip-ru'],
      customFields: emptyCustom,
      outboundCategory: 'direct',
      tunnelTags: [],
      groups,
      presets: [],
      existingRuleSetTags: [],
      existingOutbounds: [],
    });
    expect(api.singboxRouterUpdateRule).toHaveBeenCalledWith(2, {
      rule_set: ['geoip-ru'],
      action: 'route',
      outbound: 'direct',
    });
  });

  it('external: block → reject', async () => {
    (api.singboxRouterUpdateRule as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    await submitWizardEdit({
      ruleIndex: 0,
      editMode: 'external',
      selectedTemplates: ['svc:netflix'],
      customFields: emptyCustom,
      outboundCategory: 'block',
      tunnelTags: [],
      groups,
      presets,
      existingRuleSetTags: [],
      existingOutbounds: [],
    });
    expect(api.singboxRouterUpdateRule).toHaveBeenCalledWith(0, {
      rule_set: ['geosite-netflix'],
      action: 'reject',
    });
  });

  it('external: не один шаблон → ValidationError', async () => {
    await expect(
      submitWizardEdit({
        ruleIndex: 0,
        editMode: 'external',
        selectedTemplates: ['svc:netflix', 'rs:geoip-ru'],
        customFields: emptyCustom,
        outboundCategory: 'tunnel',
        tunnelTags: ['warp'],
        groups,
        presets,
        existingRuleSetTags: [],
        existingOutbounds: [],
      }),
    ).rejects.toThrow(/один шаблон/i);
  });

  it('external: шаблон не найден → ValidationError', async () => {
    await expect(
      submitWizardEdit({
        ruleIndex: 0,
        editMode: 'external',
        selectedTemplates: ['svc:missing'],
        customFields: emptyCustom,
        outboundCategory: 'tunnel',
        tunnelTags: ['warp'],
        groups,
        presets,
        existingRuleSetTags: [],
        existingOutbounds: [],
      }),
    ).rejects.toThrow(/не найден/i);
  });

  it('inline-set: обновляет существующий custom-N', async () => {
    (api.singboxRouterUpdateRuleSet as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    (api.singboxRouterUpdateRule as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    await submitWizardEdit({
      ...baseInlineArgs,
      existingInlineRuleSetTag: 'custom-1',
      wasInlineText: false,
    });
    expect(api.singboxRouterUpdateRuleSet).toHaveBeenCalledWith(
      'custom-1',
      expect.objectContaining({ tag: 'custom-1', type: 'inline' }),
    );
    expect(api.singboxRouterUpdateRule).toHaveBeenCalledWith(3, {
      rule_set: ['custom-1'],
      action: 'route',
      outbound: 'warp',
    });
    expect(api.singboxRouterAddRuleSet).not.toHaveBeenCalled();
  });

  it('inline-text (wasInlineText): создаёт новый custom-N', async () => {
    (api.singboxRouterAddRuleSet as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    (api.singboxRouterUpdateRule as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    await submitWizardEdit({
      ...baseInlineArgs,
      wasInlineText: true,
    });
    expect(api.singboxRouterAddRuleSet).toHaveBeenCalledWith(
      expect.objectContaining({ tag: 'custom-2', type: 'inline' }),
    );
    expect(api.singboxRouterUpdateRule).toHaveBeenCalledWith(3, {
      rule_set: ['custom-2'],
      action: 'route',
      outbound: 'warp',
    });
    expect(api.singboxRouterUpdateRuleSet).not.toHaveBeenCalled();
  });

  it('inline: block → reject на rule', async () => {
    (api.singboxRouterAddRuleSet as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    (api.singboxRouterUpdateRule as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    await submitWizardEdit({
      ...baseInlineArgs,
      outboundCategory: 'block',
      tunnelTags: [],
      wasInlineText: true,
      existingRuleSetTags: [],
    });
    expect(api.singboxRouterUpdateRule).toHaveBeenCalledWith(3, {
      rule_set: ['custom-1'],
      action: 'reject',
    });
  });

  it('inline: пустой список → ValidationError', async () => {
    await expect(
      submitWizardEdit({
        ...baseInlineArgs,
        customFields: { rulesList: '' },
        wasInlineText: true,
      }),
    ).rejects.toThrow(ValidationError);
  });
});
