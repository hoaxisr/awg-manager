import { notifications } from '$lib/stores/notifications';
import { setSettings as setGlobalSettings } from '$lib/stores/settings';
import type { Settings } from '$lib/types';

/** Настройки, открытые на странице: одни на все вкладки, переживают их смену. */
export class SettingsEditor {
	settings: Settings;
	saving = $state(false);

	constructor(settings: Settings) {
		this.settings = $state.raw(settings);
	}

	/**
	 * Запрос → общий стор → уведомление. `fail` — текст ошибки; для полей с
	 * валидацией на бэкенде передают `backendErrorOr(…)`.
	 */
	async save(
		request: () => Promise<Settings>,
		ok: string,
		fail: string | ((e: unknown) => string),
	): Promise<void> {
		this.saving = true;
		try {
			this.settings = await request();
			setGlobalSettings(this.settings);
			notifications.success(ok);
		} catch (e) {
			notifications.error(typeof fail === 'string' ? fail : fail(e));
		} finally {
			this.saving = false;
		}
	}
}

/** Текст ошибки бэкенда, если он есть, иначе запасной. */
export function backendErrorOr(fallback: string): (e: unknown) => string {
	return (e) => (e instanceof Error ? e.message : fallback);
}
