/**
 * Pure helpers: маппинг singboxRouter store → строки для StatusDrawer
 * секций (Зависимости, Замечания). Чистые функции — легко тестируются.
 */
import type { SingboxRouterStatus, SingboxRouterIssue } from '$lib/types';

export type DepTone = 'success' | 'error' | 'warning' | 'info' | 'muted';
/** Устойчивый идентификатор строки — по нему потребители гейтят зависимость по режиму (label — текст для глаз). */
export type DepId = 'netfilter' | 'tproxy-target';
export interface DepEntry {
  id: DepId;
  tone: DepTone;
  label: string;
  hint: string;
}

export type IssueTone = 'warning' | 'error' | 'info';
export interface IssueEntry {
  tone: IssueTone;
  /**
   * Открытый набор бэкенда (SingboxRouterIssue.kind). Нужен потребителям,
   * которые гасят СВОЙ дубликат факта, уже вложенного бэкендом в текст
   * замечания: по прозе сообщения это не различить.
   */
  kind: string;
  text: string;
  /** Серый текст-хинт справа от issue (в F3 не кликабелен; F5/F6 заменит на real CTA). */
  ctaHint?: string;
}

export function deriveDeps(status: SingboxRouterStatus | null): DepEntry[] {
  if (!status) return [];
  return [
    {
      id: 'netfilter',
      tone: status.netfilterAvailable ? 'success' : 'error',
      label: 'netfilter',
      hint: status.netfilterAvailable
        ? (status.netfilterComponentName ?? 'модуль готов')
        : 'не загружен — установите ndm-mod-netfilter',
    },
    {
      id: 'tproxy-target',
      tone: status.tproxyTargetAvailable ? 'success' : 'error',
      label: 'TPROXY target',
      hint: status.tproxyTargetAvailable
        ? 'xt_TPROXY загружен'
        : 'не загружен — kmod не доступен',
    },
  ];
}

export function deriveIssues(status: SingboxRouterStatus | null): IssueEntry[] {
  if (!status) return [];
  const issues = status.issues ?? [];
  return issues.map((i: SingboxRouterIssue) => ({
    tone: i.severity === 'error' ? ('error' as const) : ('warning' as const),
    kind: i.kind,
    text: i.message,
    ctaHint: '(в Эксперт)',
  }));
}
