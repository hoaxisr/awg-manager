<script lang="ts">
	import { Button, SegmentedControl, Toggle } from '$lib/components/ui';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';
	import { compactLayout } from '$lib/stores/compactLayout';
	import {
		settingsSectionIconMode,
		SETTINGS_SECTION_ICON_MODE_LABELS,
		type SettingsSectionIconMode,
	} from '$lib/stores/settingsSectionIconMode';
	import { serviceLetterIcons } from '$lib/stores/serviceLetterIcons';
	import { usageLevel } from '$lib/stores/settings';
	import {
		theme,
		THEME_PRESETS,
		getThemePreviewStyle,
		type ThemeCustomPalette,
		type ThemeModePreference,
		type ThemePreset,
	} from '$lib/stores/theme';
	import { Palette, ChevronDown, Check } from 'lucide-svelte';

	const PRESET_ORDER: ThemePreset[] = ['legacy', 'neo', 'mint', 'custom'];
	const LEGACY_MODE_OPTIONS: Array<{ value: ThemeModePreference; label: string }> = [
		{ value: 'system', label: 'Системная' },
		{ value: 'dark', label: 'Тёмная' },
		{ value: 'light', label: 'Светлая' },
	];
	const CUSTOM_FIELDS: Array<{ key: keyof ThemeCustomPalette; label: string; hint: string }> = [
		{ key: 'accent', label: 'Акцент', hint: 'Кнопки, активные состояния и ссылки' },
		{ key: 'background', label: 'Фон', hint: 'Базовый цвет приложения' },
		{ key: 'text', label: 'Текст', hint: 'Основной цвет текста и контраста' },
	];

	const ICON_MODE_OPTIONS: Array<{ value: SettingsSectionIconMode; label: string }> = [
		{ value: 'strict', label: SETTINGS_SECTION_ICON_MODE_LABELS.strict },
		{ value: 'harmonious', label: SETTINGS_SECTION_ICON_MODE_LABELS.harmonious },
		{ value: 'vivid', label: SETTINGS_SECTION_ICON_MODE_LABELS.vivid },
	];

	let expanded = $state(false);
	const compactForced = $derived($usageLevel === 'basic');
	const compactChecked = $derived(compactForced || $compactLayout);

	const currentThemeLabel = $derived.by(() => {
		if ($theme.preset !== 'custom') {
			if ($theme.modePreference === 'system') {
				return `${$theme.label} · Системная (${$theme.legacyMode === 'light' ? 'Светлая' : 'Тёмная'})`;
			}
			return `${$theme.label} · ${$theme.legacyMode === 'light' ? 'Светлая' : 'Тёмная'}`;
		}
		return `${$theme.label} · ${$theme.mode === 'light' ? 'Авто-светлая' : 'Авто-тёмная'}`;
	});

	function previewStyleFor(preset: ThemePreset): string {
		return getThemePreviewStyle({
			preset,
			modePreference: $theme.modePreference,
			custom: $theme.custom,
		});
	}

	function updateCustomColor(key: keyof ThemeCustomPalette, value: string): void {
		theme.updateCustom({ [key]: value });
	}
</script>

<div class="settings-block">
	<div class="card">
	<SettingsSectionLabel label="Внешний вид" icon={Palette} tone="pink" header />
	<div class="setting-row">
		<button
			type="button"
			class="collapsible-header scheme-toggle"
			aria-expanded={expanded}
			aria-controls="theme-scheme-body"
			onclick={() => (expanded = !expanded)}
		>
			<div class="flex flex-col gap-1">
				<span class="font-medium">Цветовая схема</span>
				<span class="setting-description">
					Применяется сразу и сохраняется локально в этом браузере.
				</span>
			</div>
			<span class="header-meta">
				<span class="current-theme">{currentThemeLabel}</span>
				<span class="chevron" class:open={expanded} aria-hidden="true"><ChevronDown size={14} strokeWidth={2} /></span>
			</span>
		</button>
	</div>

	{#if expanded}
		<div id="theme-scheme-body" class="collapsible-body">
			<div class="theme-grid" role="radiogroup" aria-label="Цветовая схема">
				{#each PRESET_ORDER as preset (preset)}
					{@const selected = $theme.preset === preset}
					{@const meta = THEME_PRESETS[preset]}
					<button
						type="button"
						role="radio"
						aria-checked={selected}
						class="theme-card"
						class:selected
						onclick={() => theme.setPreset(preset)}
					>
						<div class="theme-card-head">
							<div class="theme-copy">
								<div class="theme-title">{meta.label}</div>
								<div class="theme-summary">{meta.summary}</div>
							</div>
							{#if selected}
								<span class="theme-check" aria-hidden="true">
									<Check size={14} strokeWidth={3} />
								</span>
							{/if}
						</div>

						<div class="theme-preview" style={previewStyleFor(preset)}>
							<div class="preview-header">
								<span class="preview-brand"></span>
								<span class="preview-chip"></span>
							</div>
							<div class="preview-hero">
								<span class="preview-line wide"></span>
								<span class="preview-line medium"></span>
							</div>
							<div class="preview-metrics">
								<span class="preview-pill accent"></span>
								<span class="preview-pill"></span>
								<span class="preview-pill"></span>
							</div>
							<div class="preview-grid">
								<span class="preview-panel"></span>
								<span class="preview-panel"></span>
								<span class="preview-panel tall"></span>
							</div>
						</div>

					</button>
				{/each}
			</div>

			{#if $theme.supportsModeToggle}
				<div class="detail-block">
					<div class="detail-title">Режим {THEME_PRESETS[$theme.preset].label}</div>
					<SegmentedControl
						value={$theme.modePreference}
						options={LEGACY_MODE_OPTIONS}
						ariaLabel={`Режим темы ${THEME_PRESETS[$theme.preset].label}`}
						onchange={(mode) => theme.setMode(mode)}
					/>
				</div>
			{/if}

			{#if $theme.preset === 'custom'}
				<div class="detail-block custom-block">
					<div class="custom-header">
						<div>
							<div class="detail-title">Пользовательская схема</div>
							<p class="custom-hint">
								Подберите три базовых цвета, а карточки, границы и вторичный текст мы
								достроим автоматически.
							</p>
						</div>
						<Button variant="ghost" size="sm" onclick={() => theme.resetCustom()}>
							Сбросить
						</Button>
					</div>

					<div class="custom-grid">
						{#each CUSTOM_FIELDS as field (field.key)}
							<label class="color-card">
								<span class="color-label">{field.label}</span>
								<span class="color-hint">{field.hint}</span>
								<div class="color-row">
									<input
										class="color-input"
										type="color"
										value={$theme.custom[field.key]}
										oninput={(event) =>
											updateCustomColor(
												field.key,
												(event.currentTarget as HTMLInputElement).value,
											)}
									/>
									<code class="color-code">{$theme.custom[field.key]}</code>
								</div>
							</label>
						{/each}
					</div>

					<div class="auto-mode-note">
						Авто-режим: {$theme.mode === 'light' ? 'светлая схема' : 'тёмная схема'} по
						цвету фона.
					</div>
				</div>
			{/if}
		</div>
	{/if}

	<div class="setting-row icon-mode-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">Окраска иконок</span>
			<span class="setting-description">
				Заголовки настроек, политики доступа, VPN для устройств и глобус-заглушка в списках маршрутизации.
			</span>
		</div>
		<SegmentedControl
			value={$settingsSectionIconMode}
			options={ICON_MODE_OPTIONS}
			ariaLabel="Окраска иконок"
			onchange={(mode) => settingsSectionIconMode.setMode(mode)}
		/>
	</div>
	<div class="setting-row compact-layout-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">Компактный режим</span>
			<span class="setting-description">
				{#if compactForced}
					В базовом режиме всегда включена: колонка 960px и меньшие боковые отступы.
				{:else}
					Сужает интерфейс с краев, как в версии 2.8.2, фокусируя внимание на центре экрана (автоматически включается в базовом режиме).
				{/if}
			</span>
		</div>
		<Toggle
			checked={compactChecked}
			disabled={compactForced}
			onchange={(enabled) => compactLayout.setEnabled(enabled)}
		/>
	</div>
	<div class="setting-row letter-icons-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">Буквенные иконки</span>
			<span class="setting-description">
				Цветная плитка с первой буквой названия для списков маршрутизации (если не был найден логотип). 
			</span>
		</div>
		<Toggle
			checked={$serviceLetterIcons}
			onchange={(enabled) => serviceLetterIcons.setEnabled(enabled)}
		/>
	</div>
	</div>
</div>

<style>
	.compact-layout-row,
	.letter-icons-row {
		align-items: center;
	}

	@media (max-width: 640px) {
		.compact-layout-row,
		.letter-icons-row {
			flex-direction: row;
			align-items: center;
			flex-wrap: nowrap;
			gap: 0.75rem;
		}

		.compact-layout-row > *:first-child,
		.letter-icons-row > *:first-child {
			flex: 1 1 auto;
			min-width: 0;
		}
	}

	.collapsible-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		width: 100%;
		min-width: 0;
		gap: 0.5rem;
		background: transparent;
		border: 0;
		padding: 0;
		margin: 0;
		color: inherit;
		font: inherit;
		cursor: pointer;
		text-align: left;
	}

	.setting-row > .scheme-toggle {
		width: 100%;
		min-width: 0;
	}

	.scheme-toggle {
		align-items: center;
	}

	.scheme-toggle > :first-child {
		min-width: 0;
		flex: 1 1 auto;
	}

	.collapsible-header:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
		border-radius: var(--radius-sm);
	}

	.header-meta {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		color: var(--color-text-muted);
		font-size: 0.8125rem;
	}

	.current-theme {
		color: var(--color-text-secondary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		max-width: 14rem;
	}

	.chevron {
		display: inline-flex;
		width: 14px;
		height: 14px;
		transition: transform var(--t-fast) ease;
	}

	.chevron.open {
		transform: rotate(180deg);
	}

	.collapsible-body {
		border-top: 1px solid var(--color-border);
		padding-top: 0.875rem;
		padding-bottom: 0.875rem;
		border-bottom: 1px solid var(--color-border);
	}

	.theme-grid {
		--theme-grid-max: 81rem;
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(13rem, 1fr));
		gap: 0.75rem;
		/* width: min(100%, var(--theme-grid-max)); */
		max-width: 100%;
		margin-inline-start: 0;
		margin-inline-end: auto;
	}

	.theme-card {
		position: relative;
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
		padding: 0.75rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		background: var(--color-settings-control-bg);
		color: inherit;
		font: inherit;
		text-align: left;
		cursor: pointer;
		transition:
			border-color var(--t-fast) ease,
			background var(--t-fast) ease,
			transform var(--t-fast) ease;
	}

	.theme-card:hover {
		border-color: var(--color-border-hover);
		background: var(--color-bg-hover);
		transform: translateY(-1px);
	}

	.theme-card.selected {
		border-color: var(--color-accent);
		box-shadow: inset 0 0 0 1px var(--color-accent-border, var(--color-accent));
	}

	.theme-card:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}

	.theme-card-head {
		display: flex;
		gap: 0.5rem;
		align-items: flex-start;
		justify-content: space-between;
	}

	.theme-copy {
		min-width: 0;
	}

	.theme-title {
		font-size: 0.875rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.theme-summary {
		margin-top: 0.2rem;
		font-size: 0.75rem;
		line-height: 1.45;
		color: var(--color-text-muted);
	}

	.theme-check {
		width: 16px;
		height: 16px;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		color: var(--color-accent);
		flex-shrink: 0;
	}


	.theme-preview {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: calc(var(--radius-sm) + 2px);
		padding: 0.6rem;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		min-height: 8.5rem;
		box-shadow: var(--shadow);
	}

	.preview-header,
	.preview-metrics {
		display: flex;
		gap: 0.35rem;
	}

	.preview-header {
		align-items: center;
		justify-content: space-between;
	}

	.preview-brand {
		width: 3.5rem;
		height: 0.45rem;
		border-radius: var(--radius-pill);
		background: var(--color-accent);
	}

	.preview-chip {
		width: 1.75rem;
		height: 0.75rem;
		border-radius: var(--radius-pill);
		background: color-mix(in srgb, var(--color-accent) 22%, transparent);
		border: 1px solid color-mix(in srgb, var(--color-accent) 45%, transparent);
	}

	.preview-hero {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
	}

	.preview-line {
		display: block;
		height: 0.45rem;
		border-radius: var(--radius-pill);
		background: var(--color-bg-tertiary);
	}

	.preview-line.wide {
		width: 90%;
	}

	.preview-line.medium {
		width: 72%;
	}

	.preview-pill {
		flex: 1;
		height: 1rem;
		border-radius: var(--radius-pill);
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
	}

	.preview-pill.accent {
		background: color-mix(in srgb, var(--color-accent) 18%, transparent);
		border-color: color-mix(in srgb, var(--color-accent) 45%, transparent);
	}

	.preview-grid {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 0.35rem;
	}

	.preview-panel {
		display: block;
		height: 1.5rem;
		border-radius: var(--radius-sm);
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
	}

	.preview-panel.tall {
		grid-column: 1 / -1;
		height: 2rem;
	}

	.detail-block {
		margin-top: 0.9rem;
	}

	.detail-title {
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--color-text-secondary);
		margin-bottom: 0.6rem;
	}

	.detail-block :global(.segmented-control) {
		width: 100%;
	}

	.detail-block :global(.segmented-control-btn) {
		flex: 1;
		min-width: 0;
	}

	.custom-block {
		display: flex;
		flex-direction: column;
		gap: 0.85rem;
	}

	.custom-header {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 0.75rem;
	}

	.custom-hint {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-text-muted);
		line-height: 1.45;
	}

	.custom-grid {
		--theme-custom-grid-max: 62.5rem;
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 11.75rem), 1fr));
		gap: 0.75rem;
		width: min(100%, var(--theme-custom-grid-max));
		max-width: 100%;
		margin-inline-start: 0;
		margin-inline-end: auto;
	}

	.color-card {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
		padding: 0.75rem;
		border-radius: var(--radius);
		border: 1px solid var(--color-border);
		background: var(--color-settings-control-bg);
	}

	.color-label {
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--color-text-secondary);
	}

	.color-hint {
		font-size: 0.75rem;
		line-height: 1.45;
		color: var(--color-text-muted);
		min-height: 2.15rem;
	}

	.color-row {
		display: flex;
		align-items: center;
		gap: 0.6rem;
	}

	.color-input {
		width: 2.5rem;
		height: 2.5rem;
		padding: 0;
		border: 1px solid var(--color-border);
		border-radius: 10px;
		background: transparent;
		cursor: pointer;
	}

	.color-code {
		font-family: var(--font-mono, ui-monospace, monospace);
		font-size: 0.78rem;
		color: var(--color-text-primary);
	}

	.auto-mode-note {
		font-size: 0.78rem;
		color: var(--color-text-muted);
	}

	@media (max-width: 980px) {
		/* Узкая колонка настроек: компактное превью, прижато влево */
		.theme-preview {
			width: 100%;
			max-width: 15rem;
			margin-inline-start: 0;
			margin-inline-end: auto;
			min-height: 7rem;
			padding: 0.5rem;
			gap: 0.4rem;
		}

		.preview-metrics {
			gap: 0.3rem;
		}

		.preview-pill {
			height: 0.85rem;
		}

		.preview-panel {
			height: 1.25rem;
		}

		.preview-panel.tall {
			height: 1.6rem;
		}
	}

	@media (max-width: 640px) {
		.current-theme {
			max-width: 9rem;
		}

		.custom-header {
			flex-direction: column;
			align-items: stretch;
		}
	}

	@media (max-width: 640px) {
		.scheme-toggle {
			display: grid;
			grid-template-columns: minmax(0, 1fr);
			align-items: stretch;
			gap: 0.625rem;
		}

		.scheme-toggle > :first-child {
			width: 100%;
		}

		.header-meta {
			width: 100%;
			min-width: 0;
			box-sizing: border-box;
			justify-content: space-between;
			padding: 0.45rem 0.625rem;
			border: 1px solid var(--color-border);
			border-radius: var(--radius-sm);
			background: var(--color-settings-control-bg);
		}

		.current-theme {
			max-width: none;
			min-width: 0;
		}
	}

	@media (min-width: 641px) {
		.scheme-toggle {
			display: grid;
			grid-template-columns: minmax(0, 1fr);
			align-items: stretch;
			gap: 0.75rem;
		}

		.scheme-toggle > :first-child {
			width: 100%;
		}

		.header-meta {
			width: 100%;
			min-width: 0;
			box-sizing: border-box;
			justify-content: space-between;
			padding: 0.45rem 0.625rem;
			border: 1px solid var(--color-border);
			border-radius: var(--radius-sm);
			background: var(--color-settings-control-bg);
		}

		.current-theme {
			max-width: none;
			min-width: 0;
		}
	}
</style>
