// Приоритеты подписи outbound'а на карточках device-proxy (issue #465).
//
// Имя outbound'а — это НАСТРОЕННЫЙ выбор пользователя (selectedOutbound из
// конфига), а не живой selector.now: после выключения движка маршрутизации
// runtime-селектор мог деградировать (prune / graceful-fallback), и показывать
// его как «настройку» — враньё. Прецедент: about-device.ts (selectedOutbound
// первичен, activeTag — отдельная пометка «активный: X»).

import { m } from '$lib/i18n';
import type { DeviceProxyOutbound, DeviceProxyRuntime } from '$lib/types';

export interface OutboundLabelInput {
	/** Настроенный outbound инстанса (instance.selectedOutbound). */
	selectedOutbound: string;
	/** Живое runtime-состояние; null/undefined — ещё не загружено. */
	runtime?: Pick<
		DeviceProxyRuntime,
		'alive' | 'activeTag' | 'defaultTag' | 'degradedOutbound' | 'fallbackTag'
	> | null;
}

/**
 * Имя outbound'а для бейджа: конфиг первичен, runtime.defaultTag — запасной
 * вариант (например, если конфиг ещё не догрузился), «—» — когда ничего нет.
 */
export function outboundName(input: OutboundLabelInput): string {
	return input.selectedOutbound || input.runtime?.defaultTag || '—';
}

/**
 * Живой selector.now для отдельной muted-пометки «сейчас: X». Показываем
 * ТОЛЬКО когда движок жив и активный член отличается от настроенного имени —
 * иначе пометка дублирует бейдж.
 */
export function outboundNowTag(input: OutboundLabelInput): string | null {
	const rt = input.runtime;
	if (!rt?.alive || !rt.activeTag) return null;
	return rt.activeTag !== outboundName(input) ? rt.activeTag : null;
}

/**
 * Текст бейджа деградации: настроенный выход отсутствует в merged-конфиге
 * (слот-источник выключен), трафик идёт через fallback. null — нет деградации.
 * outbounds (опционально) резолвит fallback-тег в человекочитаемую подпись —
 * тем же каталогом, что бейдж и «сейчас:»; без каталога остаётся сырой тег.
 */
export function outboundDegradedText(
	input: OutboundLabelInput,
	outbounds: DeviceProxyOutbound[] = [],
): string | null {
	const rt = input.runtime;
	if (!rt?.degradedOutbound) return null;
	const via = outboundDisplayLabel(rt.fallbackTag || 'direct', outbounds);
	return m.device_proxy_outbound_unavailable_via({ via });
}

/**
 * Человекочитаемая подпись записи каталога outbound'ов — единый формат для
 * dropdown'а настроек (SettingsCard) и бейджей карточки Inbounds: kind-правила
 * повторяют исторический вид опций дропдауна.
 */
export function outboundOptionLabel(ob: DeviceProxyOutbound): string {
	switch (ob.kind) {
		case 'awg':
			return `${ob.label} · ${ob.detail}`;
		case 'router':
			return ob.detail ? `${ob.label} · ${ob.detail}` : ob.label;
		default:
			return ob.label || ob.tag;
	}
}

/**
 * Резолвит сырой тег outbound'а в человекочитаемую подпись через каталог
 * /api/deviceproxy/outbounds. Тег не найден (каталог не загружен, выход
 * удалён) или пуст — возвращается сам тег (прежнее поведение как fallback).
 */
export function outboundDisplayLabel(tag: string, outbounds: DeviceProxyOutbound[]): string {
	if (!tag) return tag;
	const ob = outbounds.find((o) => o.tag === tag);
	return ob ? outboundOptionLabel(ob) : tag;
}
