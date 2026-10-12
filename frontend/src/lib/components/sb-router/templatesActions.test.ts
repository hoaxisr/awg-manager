import { describe, it, expect, vi, beforeEach } from 'vitest';

vi.mock('$lib/api/client', () => ({
  api: {
    singboxRouterApplyPreset: vi.fn(),
    singboxRouterAddRule: vi.fn(),
  },
}));

import { api } from '$lib/api/client';
import { submitTemplates } from './templatesActions';
import type { TemplateGroup } from './templatesData';

const groups: TemplateGroup[] = [
  {
    category: 'services',
    items: [
      { id: 'svc:netflix', category: 'services', presetId: 'netflix', name: 'Netflix' },
      { id: 'svc:youtube', category: 'services', presetId: 'youtube', name: 'YouTube' },
    ],
  },
  {
    category: 'rulesets',
    items: [
      { id: 'rs:telegram', category: 'rulesets', tag: 'telegram', type: 'remote' },
    ],
  },
];

describe('templatesActions.submitTemplates', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('empty selection → empty result, no api calls', async () => {
    const r = await submitTemplates([], 'warp', groups);
    expect(r.successes).toEqual([]);
    expect(r.failures).toEqual([]);
    expect(api.singboxRouterApplyPreset).not.toHaveBeenCalled();
    expect(api.singboxRouterAddRule).not.toHaveBeenCalled();
  });

  it('svc with outbound → ApplyPreset(presetId, tag)', async () => {
    (api.singboxRouterApplyPreset as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    const r = await submitTemplates(['svc:netflix'], 'warp', groups);
    expect(api.singboxRouterApplyPreset).toHaveBeenCalledWith('netflix', 'warp');
    expect(r.successes).toEqual(['svc:netflix']);
    expect(r.failures).toEqual([]);
  });

  it('svc with block → ApplyPreset(presetId, "")', async () => {
    (api.singboxRouterApplyPreset as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    await submitTemplates(['svc:netflix'], 'block', groups);
    expect(api.singboxRouterApplyPreset).toHaveBeenCalledWith('netflix', '');
  });

  it('rs with outbound → AddRule with rule_set + outbound + action=route', async () => {
    (api.singboxRouterAddRule as ReturnType<typeof vi.fn>).mockResolvedValue(0);
    await submitTemplates(['rs:telegram'], 'warp', groups);
    expect(api.singboxRouterAddRule).toHaveBeenCalledWith({
      rule_set: ['telegram'],
      outbound: 'warp',
      action: 'route',
    });
  });

  it('rs with block → AddRule with rule_set + action=reject', async () => {
    (api.singboxRouterAddRule as ReturnType<typeof vi.fn>).mockResolvedValue(0);
    await submitTemplates(['rs:telegram'], 'block', groups);
    expect(api.singboxRouterAddRule).toHaveBeenCalledWith({
      rule_set: ['telegram'],
      action: 'reject',
    });
  });

  it('mixed selection: all success', async () => {
    (api.singboxRouterApplyPreset as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    (api.singboxRouterAddRule as ReturnType<typeof vi.fn>).mockResolvedValue(0);
    const r = await submitTemplates(['svc:netflix', 'svc:youtube', 'rs:telegram'], 'warp', groups);
    expect(r.successes.sort()).toEqual(['rs:telegram', 'svc:netflix', 'svc:youtube']);
    expect(r.failures).toEqual([]);
  });

  it('applies presets sequentially (config read-modify-write)', async () => {
    const order: string[] = [];
    (api.singboxRouterApplyPreset as ReturnType<typeof vi.fn>).mockImplementation(async (id: string) => {
      order.push(id);
    });
    await submitTemplates(['svc:netflix', 'svc:youtube'], 'warp', groups);
    expect(order).toEqual(['netflix', 'youtube']);
  });

  it('partial failure: one rejected → recorded in failures with message', async () => {
    (api.singboxRouterApplyPreset as ReturnType<typeof vi.fn>)
      .mockImplementation((id: string) => id === 'netflix'
        ? Promise.reject(new Error('boom'))
        : Promise.resolve());
    (api.singboxRouterAddRule as ReturnType<typeof vi.fn>).mockResolvedValue(0);
    const r = await submitTemplates(['svc:netflix', 'svc:youtube', 'rs:telegram'], 'warp', groups);
    expect(r.successes.sort()).toEqual(['rs:telegram', 'svc:youtube']);
    expect(r.failures).toEqual([{ id: 'svc:netflix', error: 'boom' }]);
  });
});
