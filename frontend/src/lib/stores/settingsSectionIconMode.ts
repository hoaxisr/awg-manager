import { createPersistedStore } from './persisted';
import { m } from '$lib/i18n';

export type SettingsSectionIconMode = 'strict' | 'harmonious' | 'vivid';

/** Подписи режимов — функции сообщений: читаются при рендере и следуют за языком. */
export const SETTINGS_SECTION_ICON_MODE_LABELS: Record<SettingsSectionIconMode, () => string> = {
	strict: m.settings_icon_mode_strict,
	harmonious: m.settings_icon_mode_harmonious,
	vivid: m.settings_icon_mode_vivid,
};

const DEFAULT_MODE: SettingsSectionIconMode = 'harmonious';

function isValidMode(value: string): value is SettingsSectionIconMode {
	return value === 'strict' || value === 'harmonious' || value === 'vivid';
}

const store = createPersistedStore<SettingsSectionIconMode>('awg-manager-settings-section-icon-mode', {
	defaultValue: DEFAULT_MODE,
	deserialize: (raw) => (isValidMode(raw) ? raw : DEFAULT_MODE),
	serialize: (mode) => mode,
});

export const settingsSectionIconMode = {
	subscribe: store.subscribe,
	init: store.init,
	setMode: store.set,
};
