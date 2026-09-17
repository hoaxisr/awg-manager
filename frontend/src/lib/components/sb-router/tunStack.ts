/**
 * Выбор TCP/IP-стека tun-инбаунда. Поле одно на ОБА tun-режима — fakeip-tun и
 * policy-tun читают settings.fakeipStack, — поэтому и список общий.
 *
 * Пустая строка ЗНАЧИМА: бэкенд не пишет в конфиг ключ `stack`, и sing-box
 * берёт собственный стек sing-tun (с 1.15 — дефолт, ради него всё и делалось).
 * 'system' sing-box 1.15 принимает с deprecation-warning, 1.16 потребует
 * ENABLE_DEPRECATED_TUN_STACK=true, 1.17 удалит — держим как аварийный откат.
 * 'gvisor' и 'mixed' убраны: наш бинарь собирается без тега with_gvisor.
 */
import type { TunStack } from '$lib/types';

export const TUN_STACK_OPTIONS: { value: TunStack; label: string }[] = [
	{ value: '', label: 'sing-tun (рекомендуется)' },
	{ value: 'system', label: 'system (устаревший)' },
];

/** Подпись стека в фактах/карточках: пустое значение показываем именем движка. */
export function tunStackLabel(stack: TunStack | undefined): string {
	return stack || 'sing-tun';
}

/** Подсказка под селектором; для актуального стека её нет. */
export function tunStackHint(stack: TunStack | undefined): string | undefined {
	return stack ? 'устаревший стек, sing-box удалит его в 1.17' : undefined;
}
