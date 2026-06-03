import { describe, it, expect } from 'vitest';
import { deriveDeps, deriveIssues } from './drawerData';
import type { SingboxRouterStatus, SingboxRouterIssue } from '$lib/types';

function status(partial: Partial<SingboxRouterStatus>): SingboxRouterStatus {
  return {
    enabled: false,
    installed: true,
    active: false,
    netfilterAvailable: true,
    tproxyTargetAvailable: true,
    policyName: '',
    policyExists: false,
    deviceMode: 'policy',
    snifferEnabled: false,
    deviceCount: 0,
    ruleCount: 0,
    ruleSetCount: 0,
    outboundAwgCount: 0,
    outboundCompositeCount: 0,
    final: 'direct',
    ...partial,
  };
}

describe('deriveDeps', () => {
  it('null status — пустой массив', () => {
    expect(deriveDeps(null)).toEqual([]);
  });

  it('netfilter готов', () => {
    const deps = deriveDeps(status({ netfilterAvailable: true, netfilterComponentName: 'ndm-mod-netfilter' }));
    const nf = deps.find((d) => d.label === 'netfilter');
    expect(nf?.tone).toBe('success');
    expect(nf?.hint).toMatch(/ndm-mod-netfilter/);
  });

  it('netfilter отсутствует', () => {
    const deps = deriveDeps(status({ netfilterAvailable: false }));
    const nf = deps.find((d) => d.label === 'netfilter');
    expect(nf?.tone).toBe('error');
    expect(nf?.hint).toMatch(/не загружен|отсутствует/i);
  });

  it('TPROXY готов', () => {
    const deps = deriveDeps(status({ tproxyTargetAvailable: true }));
    const tp = deps.find((d) => d.label === 'TPROXY target');
    expect(tp?.tone).toBe('success');
  });

  it('TPROXY отсутствует', () => {
    const deps = deriveDeps(status({ tproxyTargetAvailable: false }));
    const tp = deps.find((d) => d.label === 'TPROXY target');
    expect(tp?.tone).toBe('error');
  });

  it('всегда 2 entries (netfilter + TPROXY)', () => {
    expect(deriveDeps(status({}))).toHaveLength(2);
  });
});

describe('deriveIssues', () => {
  it('null status — пустой массив', () => {
    expect(deriveIssues(null)).toEqual([]);
  });

  it('пустой issues — пустой массив', () => {
    expect(deriveIssues(status({ issues: [] }))).toEqual([]);
  });

  it('warning issue из status', () => {
    const issue: SingboxRouterIssue = { severity: 'warning', kind: 'orphan-rule', message: 'Правило ссылается на удалённый outbound', ruleIndex: 2 };
    const out = deriveIssues(status({ issues: [issue] }));
    expect(out).toHaveLength(1);
    expect(out[0].tone).toBe('warning');
    expect(out[0].text).toMatch(/удалённый outbound/);
  });

  it('error issue из status', () => {
    const issue: SingboxRouterIssue = { severity: 'error', kind: 'policy-missing', message: 'NDMS policy не найдена' };
    const out = deriveIssues(status({ issues: [issue] }));
    expect(out[0].tone).toBe('error');
  });

  it('добавляет ctaHint для каждого issue', () => {
    const issue: SingboxRouterIssue = { severity: 'warning', kind: 'orphan-rule', message: 'x' };
    const out = deriveIssues(status({ issues: [issue] }));
    expect(out[0].ctaHint).toBe('(в Эксперт)');
  });

  it('несколько issues сохраняют порядок', () => {
    const issues: SingboxRouterIssue[] = [
      { severity: 'error', kind: 'policy-missing', message: 'A' },
      { severity: 'warning', kind: 'orphan-rule', message: 'B' },
    ];
    const out = deriveIssues(status({ issues }));
    expect(out.map((i) => i.text)).toEqual(['A', 'B']);
  });
});
