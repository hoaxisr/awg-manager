import { describe, it, expect, beforeEach, vi } from 'vitest';
import { get } from 'svelte/store';

const STORAGE_KEY = 'awg.sb-router.expert-collapse';

function resetEnv() {
  localStorage.clear();
  vi.resetModules();
}

describe('expertPanelCollapseStore', () => {
  beforeEach(() => {
    resetEnv();
  });

  it('defaults all sections to expanded', async () => {
    const { expertPanelCollapse } = await import('./expertPanelCollapseStore');
    expect(get(expertPanelCollapse)).toEqual({
      rules: false,
      ruleSets: false,
      outbounds: false,
      proxyGroups: false,
      dnsServers: false,
      dnsRewrite: false,
      inbounds: false,
    });
  });

  it('reads persisted state from localStorage', async () => {
    localStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({ rules: true, outbounds: true, unknown: true }),
    );
    const { expertPanelCollapse } = await import('./expertPanelCollapseStore');
    expect(get(expertPanelCollapse)).toEqual({
      rules: true,
      ruleSets: false,
      outbounds: true,
      proxyGroups: false,
      dnsServers: false,
      dnsRewrite: false,
      inbounds: false,
    });
  });

  it('toggleExpertPanelSection flips one section and persists', async () => {
    const { expertPanelCollapse, toggleExpertPanelSection } = await import('./expertPanelCollapseStore');
    toggleExpertPanelSection('dnsServers');
    expect(get(expertPanelCollapse).dnsServers).toBe(true);
    toggleExpertPanelSection('dnsServers');
    expect(get(expertPanelCollapse).dnsServers).toBe(false);
    expect(JSON.parse(localStorage.getItem(STORAGE_KEY) ?? '{}')).toMatchObject({
      dnsServers: false,
    });
  });

  it('setExpertPanelSectionCollapsed updates without noop writes', async () => {
    const { expertPanelCollapse, setExpertPanelSectionCollapsed } = await import('./expertPanelCollapseStore');
    setExpertPanelSectionCollapsed('inbounds', true);
    expect(get(expertPanelCollapse).inbounds).toBe(true);
    setExpertPanelSectionCollapsed('inbounds', true);
    expect(get(expertPanelCollapse).inbounds).toBe(true);
    setExpertPanelSectionCollapsed('inbounds', false);
    expect(get(expertPanelCollapse).inbounds).toBe(false);
  });
});
