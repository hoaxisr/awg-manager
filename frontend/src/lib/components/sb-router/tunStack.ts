/**
 * Выбор TCP/IP-стека tun-инбаунда. Поле одно на ОБА tun-режима — fakeip-tun и
 * policy-tun читают settings.fakeipStack, — поэтому и список общий.
 *
 * Пустая строка ЗНАЧИМА: бэкенд не пишет в конфиг ключ `stack`, и sing-box
 * берёт собственный стек sing-tun (с 1.15 — дефолт, ради него всё и делалось).
 * Остальные значения sing-box 1.15 принимает с deprecation-warning, 1.16
 * потребует ENABLE_DEPRECATED_TUN_STACK=true, 1.17 удалит — держим их как
 * аварийный откат, если новый стек подведёт на конкретном железе.
 */
import type { TunStack } from '$lib/types';

export const TUN_STACK_OPTIONS: { value: TunStack; label: string }[] = [
	{ value: '', label: 'sing-tun (рекомендуется)' },
	{ value: 'gvisor', label: 'gvisor (устаревший)' },
	{ value: 'system', label: 'system (устаревший)' },
	{ value: 'mixed', label: 'mixed (устаревший)' },
];

/** Подпись стека в фактах/карточках: пустое значение показываем именем движка. */
export function tunStackLabel(stack: TunStack | undefined): string {
	return stack || 'sing-tun';
}

/** Подсказка под селектором; для актуального стека её нет. */
export function tunStackHint(stack: TunStack | undefined): string | undefined {
	return stack ? 'устаревший стек, sing-box удалит его в 1.17' : undefined;
}
