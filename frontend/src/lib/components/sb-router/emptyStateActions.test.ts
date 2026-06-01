import { describe, it, expect, vi, beforeEach } from 'vitest';

vi.mock('$lib/api/client', () => ({
  api: {
    singboxRouterEnable: vi.fn(),
    singboxRouterPutRouteFinal: vi.fn(),
    singboxRouterListDNSServers: vi.fn(async () => []),
    singboxRouterAddDNSServer: vi.fn(),
    singboxRouterUpdateDNSServer: vi.fn(),
    singboxRouterListRules: vi.fn(async () => []),
    singboxRouterListDNSRules: vi.fn(async () => []),
    singboxRouterAddDNSRule: vi.fn(),
    singboxRouterUpdateDNSRule: vi.fn(),
    singboxRouterDeleteDNSRule: vi.fn(),
    singboxRouterGetDNSGlobals: vi.fn(async () => ({ final: '', strategy: 'ipv4_only' })),
    singboxRouterPutDNSGlobals: vi.fn(),
  },
}));

vi.mock('./addWizardActions', () => ({
  submitWizard: vi.fn(async () => ({ successes: ['svc:netflix'], failures: [] })),
}));

vi.mock('./settingsActions', () => ({
  mergeAndSaveSettings: vi.fn(async () => {}),
}));

vi.mock('$lib/stores/singboxRouter', () => {
  const settings = { subscribe: vi.fn(() => () => {}) };
  return {
    singboxRouter: {
      settings,
      loadAll: vi.fn(async () => {}),
    },
  };
});

import { api } from '$lib/api/client';
import { singboxRouter } from '$lib/stores/singboxRouter';
import { submitWizard } from './addWizardActions';
import { mergeAndSaveSettings } from './settingsActions';
import { finishSetup, ensureTunnelDnsInfra, syncTunnelDnsRule } from './emptyStateActions';

describe('emptyStateActions', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('finishSetup: правила(tunnel) → final=direct → bake defaults(all) → enable → loadAll', async () => {
    const empty = { rulesList: '' };
    const res = await finishSetup({
      tunnelTag: 'wg-nl',
      selectedTemplates: ['svc:netflix'],
      customFields: empty,
      groups: [],
      existingRuleSetTags: [],
    });
    expect(submitWizard).toHaveBeenCalledWith(
      expect.objectContaining({ outboundCategory: 'tunnel', tunnelTag: 'wg-nl', selectedTemplates: ['svc:netflix'] }),
    );
    expect(api.singboxRouterPutRouteFinal).toHaveBeenCalledWith('direct');
    expect(api.singboxRouterPutDNSGlobals).toHaveBeenCalledWith(
      expect.objectContaining({ final: 'dns-direct' }),
    );
    expect(api.singboxRouterEnable).toHaveBeenCalled();
    expect(mergeAndSaveSettings).toHaveBeenCalledWith(
      expect.objectContaining({ deviceMode: 'all', wanAutoDetect: true, wanInterface: '', snifferEnabled: true }),
    );
    expect(api.singboxRouterEnable).toHaveBeenCalled();
    expect(singboxRouter.loadAll).toHaveBeenCalled();
    expect(res.successes).toContain('svc:netflix');
  });
});

describe('syncTunnelDnsRule', () => {
  beforeEach(() => vi.clearAllMocks());

  it('туннельные правила → upsert union в dns-tunnel (add, если правила нет)', async () => {
    (api.singboxRouterListDNSServers as ReturnType<typeof vi.fn>).mockResolvedValue([
      { tag: 'dns-tunnel', type: 'udp', server: '9.9.9.9', detour: 'wg-nl' },
    ]);
    (api.singboxRouterListRules as ReturnType<typeof vi.fn>).mockResolvedValue([
      { rule_set: ['geosite-netflix'], outbound: 'wg-nl', action: 'route' },
      { domain_suffix: ['x.com'], outbound: 'wg-nl', action: 'route' },
      { rule_set: ['ads'], action: 'reject' },
      { domain_suffix: ['y.com'], outbound: 'direct', action: 'route' },
    ]);
    (api.singboxRouterListDNSRules as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    await syncTunnelDnsRule();
    expect(api.singboxRouterAddDNSRule).toHaveBeenCalledWith(
      expect.objectContaining({ rule_set: ['geosite-netflix'], domain_suffix: ['x.com'], server: 'dns-tunnel' }),
    );
  });

  it('существующее dns-tunnel правило обновляется (не добавляется); лишние удаляются', async () => {
    (api.singboxRouterListDNSServers as ReturnType<typeof vi.fn>).mockResolvedValue([
      { tag: 'dns-tunnel', type: 'udp', server: '9.9.9.9', detour: 'wg-nl' },
    ]);
    (api.singboxRouterListRules as ReturnType<typeof vi.fn>).mockResolvedValue([
      { rule_set: ['geosite-netflix'], outbound: 'wg-nl', action: 'route' },
    ]);
    (api.singboxRouterListDNSRules as ReturnType<typeof vi.fn>).mockResolvedValue([
      { rule_set: ['old'], server: 'dns-tunnel' },
      { domain_suffix: ['z.com'], server: 'other' },
      { rule_set: ['dup'], server: 'dns-tunnel' },
    ]);
    await syncTunnelDnsRule();
    expect(api.singboxRouterUpdateDNSRule).toHaveBeenCalledWith(
      0,
      expect.objectContaining({ rule_set: ['geosite-netflix'], server: 'dns-tunnel' }),
    );
    expect(api.singboxRouterDeleteDNSRule).toHaveBeenCalledWith(2);
    expect(api.singboxRouterAddDNSRule).not.toHaveBeenCalled();
  });

  it('пустой агрегат → удаляет все dns-tunnel правила', async () => {
    (api.singboxRouterListRules as ReturnType<typeof vi.fn>).mockResolvedValue([
      { domain_suffix: ['y.com'], outbound: 'direct', action: 'route' },
    ]);
    (api.singboxRouterListDNSRules as ReturnType<typeof vi.fn>).mockResolvedValue([
      { rule_set: ['old'], server: 'dns-tunnel' },
    ]);
    await syncTunnelDnsRule();
    expect(api.singboxRouterDeleteDNSRule).toHaveBeenCalledWith(0);
    expect(api.singboxRouterAddDNSRule).not.toHaveBeenCalled();
    expect(api.singboxRouterUpdateDNSRule).not.toHaveBeenCalled();
  });
});

describe('ensureTunnelDnsInfra', () => {
  beforeEach(() => vi.clearAllMocks());

  it('создаёт dns-direct + dns-tunnel(detour) + final=dns-direct', async () => {
    (api.singboxRouterListDNSServers as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    (api.singboxRouterGetDNSGlobals as ReturnType<typeof vi.fn>).mockResolvedValue({ final: '', strategy: 'ipv4_only' });
    await ensureTunnelDnsInfra('wg-nl');
    expect(api.singboxRouterAddDNSServer).toHaveBeenCalledWith(expect.objectContaining({ tag: 'dns-direct', server: '77.88.8.8' }));
    expect(api.singboxRouterAddDNSServer).toHaveBeenCalledWith(expect.objectContaining({ tag: 'dns-tunnel', server: '9.9.9.9', detour: 'wg-nl' }));
    expect(api.singboxRouterPutDNSGlobals).toHaveBeenCalledWith(expect.objectContaining({ final: 'dns-direct', strategy: 'ipv4_only' }));
  });

  it('по умолчанию strategy=prefer_ipv4, когда стратегия не задана', async () => {
    (api.singboxRouterListDNSServers as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    (api.singboxRouterGetDNSGlobals as ReturnType<typeof vi.fn>).mockResolvedValue({ final: '', strategy: '' });
    await ensureTunnelDnsInfra('wg-nl');
    expect(api.singboxRouterPutDNSGlobals).toHaveBeenCalledWith(expect.objectContaining({ final: 'dns-direct', strategy: 'prefer_ipv4' }));
  });
});
