import { m } from '$lib/i18n';
import type { SubscriptionMode } from '$lib/types';

/** Сервер выбирает sing-box (urltest / fallback): ручного выбора нет, бэкенд ответит 409. */
export function isAutoSelectMode(mode: SubscriptionMode): boolean {
	return mode !== 'selector';
}

const MODE_LABELS: Record<SubscriptionMode, string> = {
	selector: 'Selector',
	urltest: 'URLTest',
	fallback: 'Fallback',
};

export function subscriptionModeLabel(mode: SubscriptionMode): string {
	return MODE_LABELS[mode] ?? MODE_LABELS.selector;
}

/** Пояснение, как в режиме выбирается сервер. */
export function subscriptionModeHint(mode: SubscriptionMode): string {
	switch (mode) {
		case 'urltest':
			return m.subscriptions_members_hint_urltest();
		case 'fallback':
			return m.subscriptions_form_mode_fallback_desc();
		default:
			return m.subscriptions_members_hint_selector();
	}
}
