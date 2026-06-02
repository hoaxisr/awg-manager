/**
 * Унифицированные типы для RuleCard panel (F2).
 *
 * Источник дизайна: singbox-router/project/parts/RuleCard.jsx
 * MatcherChip type/value pairs соответствуют labelMap из RuleCard.jsx:
 *   domain → 'домен', ip → 'IP', port → 'порт', set → 'набор', src → 'источник', geoip → 'geoip'
 */

export type RuleAction = 'route' | 'block' | 'direct' | 'sniff' | 'hijack-dns';

/** Категория matcher chip — соответствует labelMap из дизайна. */
export type MatcherKind = 'domain' | 'ip' | 'port' | 'ruleset' | 'src' | 'protocol' | 'private';

export interface MatcherChip {
  /** Категория для подписи слева ("домен:", "IP:" etc.) */
  kind: MatcherKind;
  /** Отображаемое значение справа от подписи */
  label: string;
  /** Mono шрифт для технических значений (IP/port). domain/ruleset — sans. */
  mono?: boolean;
}

export type OutboundKind = 'tunnel' | 'awg' | 'direct' | 'block' | 'composite' | 'unknown' | 'sniff' | 'hijack-dns';

export interface OutboundDisplay {
  /** Имя outbound'а из singbox config (raw key) */
  name: string;
  /** Локализованный label для tile ("WARP", "Прямо", "Блок") */
  label: string;
  /** Тип — определяет визуальный вариант OutboundTile */
  kind: OutboundKind;
}

export interface RuleCardData {
  /** Стабильный ID для Svelte each-block key */
  id: string;
  /** Service key из SERVICES палитры; 'custom' если не определён */
  serviceKey: string;
  /** Заголовок карточки (название сервиса или fallback "Правило N") */
  title: string;
  /** Опциональный subtitle — например, "5 доменов" если матчеров много */
  subtitle?: string;
  /** Все matcher chip'ы (без лимита; RuleCard.svelte сам обрежет до 4) */
  matchers: MatcherChip[];
  /** Действие правила */
  action: RuleAction;
  /** Куда идёт трафик */
  outbound: OutboundDisplay;
  /** System rule (ip_is_private bypass, sniff, hijack-dns) — рендерится muted */
  isSystem: boolean;
}
