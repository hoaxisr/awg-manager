import { describe, it, expect, beforeEach, vi } from 'vitest';
import { get } from 'svelte/store';

vi.mock('$app/navigation', () => ({
  goto: vi.fn((url: string, opts?: { replaceState?: boolean }) => {
    if (opts?.replaceState) {
      window.history.replaceState({}, '', url);
    } else {
      window.history.pushState({}, '', url);
    }
    return Promise.resolve();
  }),
}));

function resetEnv(url: string) {
  window.history.replaceState({}, '', url);
  vi.resetModules();
}

describe('addWizardStore', () => {
  beforeEach(() => {
    resetEnv('/');
  });

  it('default state: closed, all empty', async () => {
    const m = await import('./addWizardStore');
    expect(get(m.addWizardOpen)).toBe(false);
    expect(get(m.wizardOutboundCategory)).toBe(null);
    expect(get(m.wizardTunnelTags)).toEqual([]);
    const c = get(m.wizardCustom);
    expect(c.rulesList).toBe('');
  });

  it('openAddWizard sets URL ?add=1 on the current path + open=true (push)', async () => {
    // Визард живёт на /sb/routing и НЕ переписывает путь: параметры визарда
    // относительны текущего адреса (раньше он дописывал ?tab=singbox контейнера).
    resetEnv('/sb/routing?view=fakeip');
    const m = await import('./addWizardStore');
    m.openAddWizard();
    expect(get(m.addWizardOpen)).toBe(true);
    const url = new URL(window.location.href);
    expect(url.pathname).toBe('/sb/routing');
    expect(url.searchParams.get('add')).toBe('1');
    expect(url.searchParams.get('view')).toBe('fakeip');
    expect(url.searchParams.get('tab')).toBeNull();
    expect(window.history.length).toBeGreaterThan(1);
  });

  it('closeAddWizard removes add from URL + clears all state', async () => {
    resetEnv('/sb/routing');
    const m = await import('./addWizardStore');
    m.openAddWizard();
    m.setOutboundCategory('tunnel');
    m.toggleTunnelTag('warp');
    m.updateCustomField('rulesList', 'a.com');
    m.closeAddWizard();
    expect(get(m.addWizardOpen)).toBe(false);
    expect(get(m.wizardOutboundCategory)).toBe(null);
    expect(get(m.wizardTunnelTags)).toEqual([]);
    expect(get(m.wizardCustom).rulesList).toBe('');
    expect(new URL(window.location.href).searchParams.get('add')).toBeNull();
    expect(new URL(window.location.href).pathname).toBe('/sb/routing');
  });

  it('popstate without ?add= closes wizard overlay', async () => {
    resetEnv('/sb/routing?add=1');
    const m = await import('./addWizardStore');
    m.openAddWizard();
    expect(get(m.addWizardOpen)).toBe(true);
    // jsdom не меняет location на history.back() — эмулируем URL после «назад».
    window.history.replaceState({}, '', '/sb/routing');
    window.dispatchEvent(new PopStateEvent('popstate'));
    expect(get(m.addWizardOpen)).toBe(false);
    expect(new URL(window.location.href).pathname).toBe('/sb/routing');
    expect(new URL(window.location.href).searchParams.get('add')).toBeNull();
  });

  it('setOutboundCategory updates', async () => {
    const m = await import('./addWizardStore');
    m.setOutboundCategory('tunnel');
    expect(get(m.wizardOutboundCategory)).toBe('tunnel');
    m.setOutboundCategory('block');
    expect(get(m.wizardOutboundCategory)).toBe('block');
    m.setOutboundCategory(null);
    expect(get(m.wizardOutboundCategory)).toBe(null);
  });

  it('toggleTunnelTag adds and removes', async () => {
    const m = await import('./addWizardStore');
    m.toggleTunnelTag('warp');
    expect(get(m.wizardTunnelTags)).toEqual(['warp']);
    m.toggleTunnelTag('awg10');
    expect(get(m.wizardTunnelTags)).toEqual(['warp', 'awg10']);
    m.toggleTunnelTag('warp');
    expect(get(m.wizardTunnelTags)).toEqual(['awg10']);
  });

  it('setTunnelTags replaces selection', async () => {
    const m = await import('./addWizardStore');
    m.setTunnelTags(['warp', 'awg10']);
    expect(get(m.wizardTunnelTags)).toEqual(['warp', 'awg10']);
    m.setTunnelTags([]);
    expect(get(m.wizardTunnelTags)).toEqual([]);
  });

  it('updateCustomField пишет rulesList', async () => {
    const m = await import('./addWizardStore');
    m.updateCustomField('rulesList', '*.netflix.com\n8.8.8.8');
    expect(get(m.wizardCustom).rulesList).toBe('*.netflix.com\n8.8.8.8');
  });

  it('resetWizardState очищает rulesList', async () => {
    const m = await import('./addWizardStore');
    m.updateCustomField('rulesList', 'foo.com');
    m.resetWizardState();
    expect(get(m.wizardCustom).rulesList).toBe('');
  });

  it('resetWizardState keeps open, clears selection/category/tunnel/custom', async () => {
    const m = await import('./addWizardStore');
    m.openAddWizard();
    m.setOutboundCategory('tunnel');
    m.toggleTunnelTag('warp');
    m.updateCustomField('rulesList', 'a.com');
    m.resetWizardState();
    expect(get(m.addWizardOpen)).toBe(true);
    expect(get(m.wizardOutboundCategory)).toBe(null);
    expect(get(m.wizardTunnelTags)).toEqual([]);
    expect(get(m.wizardCustom).rulesList).toBe('');
  });

  it('module init с URL ?add=1 не восстанавливает визард', async () => {
    resetEnv('/?add=1');
    const m = await import('./addWizardStore');
    expect(get(m.addWizardOpen)).toBe(false);
  });

  it('openEditWizard: prefill + edit state', async () => {
    const m = await import('./addWizardStore');
    m.openEditWizard(5, {
      editMode: 'inline',
      rulesList: 'foo.com',
      outboundCategory: 'tunnel',
      tunnelTags: ['warp', 'awg10'],
      existingInlineRuleSetTag: 'custom-1',
      wasInlineText: false,
    });
    expect(get(m.addWizardOpen)).toBe(true);
    expect(get(m.wizardEditRuleIndex)).toBe(5);
    expect(get(m.wizardEditMode)).toBe('inline');
    expect(get(m.wizardExistingInlineRuleSetTag)).toBe('custom-1');
    expect(get(m.wizardWasInlineText)).toBe(false);
    expect(get(m.wizardCustom).rulesList).toBe('foo.com');
    expect(get(m.wizardOutboundCategory)).toBe('tunnel');
    expect(get(m.wizardTunnelTags)).toEqual(['warp', 'awg10']);
  });

  it('openEditWizard external mode', async () => {
    const m = await import('./addWizardStore');
    m.openEditWizard(2, {
      editMode: 'external',
      rulesList: '',
      outboundCategory: 'block',
      tunnelTags: [],
      wasInlineText: false,
    });
    expect(get(m.wizardEditMode)).toBe('external');
    expect(get(m.wizardOutboundCategory)).toBe('block');
  });

  it('closeAddWizard clears edit state', async () => {
    const m = await import('./addWizardStore');
    m.openEditWizard(1, {
      editMode: 'inline',
      rulesList: 'a.com',
      outboundCategory: 'direct',
      tunnelTags: [],
      wasInlineText: true,
    });
    m.closeAddWizard();
    expect(get(m.wizardEditRuleIndex)).toBe(null);
    expect(get(m.wizardEditMode)).toBe(null);
    expect(get(m.wizardWasInlineText)).toBe(false);
  });

  it('openAddWizard clears prior edit state', async () => {
    const m = await import('./addWizardStore');
    m.openEditWizard(9, {
      editMode: 'inline',
      rulesList: 'x.com',
      outboundCategory: 'tunnel',
      tunnelTags: ['warp'],
    });
    m.openAddWizard();
    expect(get(m.wizardEditRuleIndex)).toBe(null);
    expect(get(m.wizardEditMode)).toBe(null);
  });

  it('closeAddWizard clears templates selection (утечка edit-prefill)', async () => {
    const m = await import('./addWizardStore');
    const t = await import('./templatesStore');
    t.setTemplateSelection(['svc:discord']);
    m.openEditWizard(2, {
      editMode: 'external',
      rulesList: '',
      outboundCategory: 'tunnel',
      tunnelTags: ['warp'],
    });
    m.closeAddWizard();
    expect(get(t.templatesSelection).size).toBe(0);
  });
});
