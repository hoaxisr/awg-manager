export const SETTINGS_TABS = ['general', 'updates', 'components', 'access', 'logs', 'system'] as const;
export type SettingsTab = (typeof SETTINGS_TABS)[number];

export interface SettingsLink {
	tab: SettingsTab;
	/** id элемента, к которому прокрутить и который подсветить. */
	anchor?: string;
	/** Одноразовый токен подтверждения смены адреса панели. */
	listenConfirm?: string;
}

/** Куда ведёт бейдж версии, когда есть обновление. */
export const UPDATE_SETTINGS_HREF = '/settings#awgm-update';
export const LOGGING_SETTINGS_HREF = '/settings#logging';

/**
 * Глубокая ссылка на настройки → вкладка и блок на ней. Ссылки старше вкладок
 * (закладки, тосты, бейдж версии) вкладку не называют — её знает только эта
 * функция. null — обычный адрес (`/settings`, `?tab=…`): ведёт сам `Tabs`.
 */
export function resolveSettingsLink(url: URL): SettingsLink | null {
	const listenConfirm = url.hash.match(/listen-confirm=([0-9a-f]{16,})/)?.[1];
	if (listenConfirm) return { tab: 'access', listenConfirm };
	if (url.searchParams.has('mode')) return { tab: 'general', anchor: 'usage-level' };
	if (url.searchParams.has('feedbackFab')) return { tab: 'updates', anchor: 'feedback-fab' };
	if (url.hash === '#downloads' || url.searchParams.get('highlight') === 'downloads') {
		return { tab: 'updates', anchor: 'downloads' };
	}
	if (url.hash === '#awgm-update') return { tab: 'updates', anchor: 'awgm-update' };
	if (url.hash === '#logging') return { tab: 'logs', anchor: 'logging' };
	return null;
}

/** Адрес вкладки — им заменяется разобранная ссылка, чтобы она не сработала повторно. */
export function settingsTabHref(tab: SettingsTab): string {
	return tab === SETTINGS_TABS[0] ? '/settings' : `/settings?tab=${tab}`;
}

/** Прокрутить к блоку настроек и подсветить его. Нет такого блока на этом уровне — ничего. */
export function revealSettingsTarget(id: string): void {
	const el = document.getElementById(id);
	if (!el) return;
	el.scrollIntoView({ behavior: 'smooth', block: 'center' });
	el.classList.remove('highlighted');
	void el.offsetWidth; // перезапуск анимации при повторной ссылке
	el.classList.add('settings-highlight-target', 'highlighted');
	// animationend всплывает: анимация потомка не должна гасить подсветку.
	const onEnd = (e: AnimationEvent) => {
		if (e.target !== el) return;
		el.classList.remove('highlighted');
		el.removeEventListener('animationend', onEnd);
	};
	el.addEventListener('animationend', onEnd);
}
