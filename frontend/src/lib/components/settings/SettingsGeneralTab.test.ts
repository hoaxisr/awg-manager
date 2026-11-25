import { describe, it, expect, vi } from 'vitest';

vi.hoisted(() => {
	Object.defineProperty(window, 'matchMedia', {
		configurable: true,
		value: (query: string) => ({ matches: false, media: query, addEventListener: () => {}, removeEventListener: () => {} }),
	});
});
import { render, screen } from '@testing-library/svelte';
import { m } from '$lib/i18n';
import type { Settings } from '$lib/types';
import type { UsageLevel } from '$lib/types/usageLevel';
import SettingsGeneralTab from './SettingsGeneralTab.svelte';
import { SettingsEditor } from './settingsEditor.svelte';

describe('SettingsGeneralTab', () => {
	// Карточка «Внешний вид» есть не на всех уровнях, а язык обязан быть всегда.
	it.each<UsageLevel>(['basic', 'advanced', 'expert'])('%s: язык, формат даты и перезапуск — по одному разу', (usageLevel) => {
		render(SettingsGeneralTab, { props: { editor: new SettingsEditor({ usageLevel } as unknown as Settings) } });
		for (const label of [m.settings_language_label(), m.settings_date_format_label(), m.settings_page_restart_label()]) {
			expect(screen.getAllByText(label)).toHaveLength(1);
		}
		expect(screen.queryAllByText(m.settings_theme_title())).toHaveLength(usageLevel === 'basic' ? 0 : 1);
	});
});
