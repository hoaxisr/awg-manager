/**
 * Pure helpers: маппинг singboxRouter store → строки для StatusDrawer
 * секций (Зависимости, Замечания). Чистые функции — легко тестируются.
 */
import { m } from '$lib/i18n';
import type { SingboxRouterStatus, SingboxRouterIssue } from '$lib/types';

export type DepTone = 'success' | 'error' | 'warning' | 'info' | 'muted';
export interface DepEntry {
  tone: DepTone;
  label: string;
  hint: string;
}

export type IssueTone = 'warning' | 'error' | 'info';
export interface IssueEntry {
  tone: IssueTone;
  text: string;
  /** Серый текст-хинт справа от issue (в F3 не кликабелен; F5/F6 заменит на real CTA). */
  ctaHint?: string;
}

export function deriveDeps(status: SingboxRouterStatus | null): DepEntry[] {
  if (!status) return [];
  return [
    {
      tone: status.netfilterAvailable ? 'success' : 'error',
      label: 'netfilter',
      hint: status.netfilterAvailable
        ? (status.netfilterComponentName ?? m.sb_router_deps_module_ready())
        : m.sb_router_deps_netfilter_missing(),
    },
    {
      tone: status.tproxyTargetAvailable ? 'success' : 'error',
      label: 'TPROXY target',
      hint: status.tproxyTargetAvailable
        ? m.sb_router_deps_tproxy_loaded()
        : m.sb_router_deps_tproxy_missing(),
    },
  ];
}

export function deriveIssues(status: SingboxRouterStatus | null): IssueEntry[] {
  if (!status) return [];
  const issues = status.issues ?? [];
  return issues.map((i: SingboxRouterIssue) => ({
    tone: i.severity === 'error' ? ('error' as const) : ('warning' as const),
    text: i.message,
    ctaHint: m.sb_router_issue_cta_expert(),
  }));
}
