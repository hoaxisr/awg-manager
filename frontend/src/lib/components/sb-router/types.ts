/**
 * Унифицированные типы для RuleCard panel (F2).
 *
 * Источник дизайна: singbox-router/project/parts/RuleCard.jsx
 * MatcherChip type/value pairs соответствуют labelMap из RuleCard.jsx:
 *   domain → 'домен', ip → 'IP', port → 'порт', set → 'набор', src → 'источник', geoip → 'geoip'
 */

import type { RuleSetDisplayType } from '$lib/utils/ruleSetType';
import type { RuleSimplicity } from './simpleRule';

export type RuleAction = 'route' | 'block' | 'direct' | 'sniff' | 'hijack-dns' | 'udp-timeout';

/** Категория matcher chip — соответствует labelMap из дизайна. */
export type MatcherKind = 'domain' | 'ip' | 'port' | 'ruleset' | 'src' | 'protocol' | 'private';

export interface MatcherChip {
  /** Категория для подписи слева ("домен:", "IP:" etc.) */
  kind: MatcherKind;
  /** Отображаемое значение справа от подписи */
  label: string;
  /** Mono шрифт для технических значений (IP/port). domain/ruleset — sans. */
  mono?: boolean;
  /** Тег rule_set в конфиге (только kind === 'ruleset') — для открытия редактора набора */
  rulesetTag?: string;
  /** Тип rule_set для иконки в простом режиме (только kind === 'ruleset') */
  rulesetType?: RuleSetDisplayType;
}

export type OutboundKind =
	| 'proxy'
	| 'tunnel'
	| 'awg'
	| 'subscription'
	| 'direct'
	| 'block'
	| 'composite'
	| 'via-route'
	| 'unknown'
	| 'sniff'
	| 'hijack-dns'
	| 'udp-timeout';

export interface OutboundDisplay {
  /** Имя outbound'а из singbox config (raw key) */
  name: string;
  /** Локализованный label для tile — основной текст без metaSuffix */
  label: string;
  /** Суффикс в скобках: sub, t2s0 — единый рендер в OutboundChipLabel */
  metaSuffix?: string;
  /** Тип — определяет визуальный вариант OutboundTile */
  kind: OutboundKind;
  /** Цветовая категория бейджа (если не задан — из kind) */
  tone?: import('./outboundTileTone').OutboundTileTone;
  /** Подсказка для invalid/warning chip */
  invalidHint?: string;
  /** Тип composite (selector / urltest / loadbalance) */
  compositeType?: 'selector' | 'urltest' | 'loadbalance';
  /** Активный участник composite (clash now / selector / urltest) */
  activeMemberLabel?: string;
  activeMemberTitle?: string;
  /** proxyInterface активного участника — суффикс в chip composite */
  activeMemberMetaSuffix?: string;
  /** Остальные участники — в +N */
  otherMemberLabels?: string[];
  otherMemberTitles?: string[];
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
  /** Правило сгенерировано awg-manager (awgm_managed): reconcile его перепишет, bulk-outbound отвергнет */
  isManaged: boolean;
  /** Эвристика простого правила для beginner UI */
  simplicity: RuleSimplicity;
  /** Пояснение при наведении (только системные правила) */
  tooltip?: string;
}
