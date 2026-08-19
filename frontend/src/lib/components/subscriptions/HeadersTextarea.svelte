<script lang="ts">
	import { ALL_HEADERS_PRESET } from './headersParser';
	import { Dropdown } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import type { SubscriptionHeaderProfile } from '$lib/types';

	interface Props {
		value: string;
	}
	let { value = $bindable('') }: Props = $props();

	let showHelp = $state(false);
	// Пресеты приходят с бэкенда — тот же список, которым детект перебирает
	// профили клиентов. Второй копии на фронте нет.
	let profiles = $state<SubscriptionHeaderProfile[]>([]);

	$effect(() => {
		void api
			.listSubscriptionHeaderProfiles()
			.then((list) => (profiles = Array.isArray(list) ? list : []))
			.catch(() => (profiles = []));
	});

	let presetOptions = $derived([
		...profiles.map((p) => ({ value: p.kind, label: p.label })),
		{ value: 'all', label: 'Полный шаблон заголовков' },
	]);

	function applyPreset(preset: string): void {
		if (value.trim() && !confirm('Заменить текущие заголовки пресетом?')) return;
		value = preset;
	}

	async function pickPreset(kind: string): Promise<void> {
		if (kind === 'all') {
			applyPreset(ALL_HEADERS_PRESET);
			return;
		}
		// Перезапрос — ради свежих HWID / модели устройства: часть значений
		// профиля генерируется на каждый вызов.
		const fresh = await api.listSubscriptionHeaderProfiles().catch(() => profiles);
		const profile = (Array.isArray(fresh) ? fresh : profiles).find((p) => p.kind === kind);
		if (profile) applyPreset(profile.headersText);
	}
</script>

<div class="head">
	<div class="head-label">
		<label class="lbl" for="hdr">Заголовки запроса</label>
		<span class="info-hint">
			<button
				type="button"
				class="info-trigger"
				aria-label="Подсказка по заголовкам"
				aria-expanded={showHelp}
				onclick={() => (showHelp = !showHelp)}
			>
				<svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
					<circle cx="8" cy="8" r="7" fill="none" stroke="currentColor" stroke-width="1.4" />
					<circle cx="8" cy="4.8" r="0.95" fill="currentColor" />
					<rect x="7.25" y="6.9" width="1.5" height="4.8" rx="0.75" fill="currentColor" />
				</svg>
			</button>
		</span>
	</div>
	<div class="head-actions">
		<Dropdown
			placeholder="Подставить пресет"
			options={presetOptions}
			onchange={(v) => void pickPreset(v)}
		/>
	</div>
</div>

{#if showHelp}
	<div class="help">
		<div class="help-row">
			Один заголовок на строку, формат <code>Имя: Значение</code>.
			Пустые строки и строки с <code>#</code> игнорируются.
		</div>
		<div class="help-row">
			<span class="help-lbl">Поддерживаемые заголовки</span> — любые,
			кроме служебных: <code>Connection</code>, <code>Host</code>,
			<code>Content-Length</code>, <code>Transfer-Encoding</code>,
			<code>Upgrade</code>. Они управляются Go-клиентом и тихо игнорируются.
		</div>
		<div class="help-row">
			<span class="help-lbl">Часто требуются провайдерами</span>:
			<code>User-Agent</code>, <code>X-HWID</code>,
			<code>X-Device-OS</code>, <code>X-Device-Locale</code>,
			<code>X-Device-Model</code>, <code>X-Ver-OS</code>,
			<code>X-App-Version</code>, <code>Accept-Encoding</code>,
			<code>X-Real-IP</code>, <code>X-Forwarded-For</code>.
		</div>
	</div>
{/if}

<textarea
	id="hdr"
	class="textarea"
	bind:value
	placeholder={'# Пример:\nUser-Agent: mihomo/v1.19.20'}
	rows="8"
></textarea>

<style>
	.head {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 0.4rem;
		gap: 0.5rem;
	}
	.head-label {
		display: inline-flex;
		align-items: center;
		min-width: 0;
	}
	.head-actions {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-shrink: 0;
	}
	.head-actions :global(.field) {
		min-width: 0;
	}
	.head-actions :global(.trigger) {
		width: auto;
		white-space: nowrap;
	}
	.lbl {
		font-size: 13px;
		font-weight: 500;
		color: var(--color-text-secondary);
	}
	.info-hint {
		position: relative;
		display: inline-flex;
		vertical-align: middle;
		margin-left: 0.25rem;
	}

	.info-trigger {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 1.05rem;
		height: 1.05rem;
		padding: 0;
		border: none;
		border-radius: 50%;
		background: transparent;
		color: var(--text-muted, var(--color-text-muted));
		cursor: pointer;
		transition: color 0.12s ease;
	}

	.info-trigger:hover,
	.info-trigger[aria-expanded='true'] {
		color: var(--accent);
	}

	.info-trigger:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 2px;
		border-radius: 50%;
	}
	.help {
		margin-bottom: 0.6rem;
		padding: 0.7rem 0.8rem;
		background: var(--color-bg-secondary, var(--color-bg-primary));
		border: 1px solid var(--color-border);
		border-radius: 4px;
		font-size: 13px;
		color: var(--color-text-muted);
		line-height: 1.5;
	}
	.help-row {
		margin-bottom: 0.4rem;
	}
	.help-row:last-child {
		margin-bottom: 0;
	}
	.help-lbl {
		color: var(--color-text-primary);
		font-weight: 500;
	}
	.help code {
		background: var(--color-bg-primary);
		padding: 0.05rem 0.3rem;
		border-radius: 3px;
		font-family: var(--font-mono, ui-monospace, monospace);
		font-size: 12px;
		color: var(--color-text-primary);
	}
	.textarea {
		width: 100%;
		min-height: 180px;
		font-family: var(--font-mono, ui-monospace, monospace);
		font-size: 13px;
		line-height: 1.45;
		padding: 0.7rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: 4px;
		color: var(--color-text-primary);
		resize: vertical;
	}

	@media (max-width: 640px) {
		.head {
			flex-wrap: wrap;
			align-items: flex-start;
		}

		.head-actions {
			width: 100%;
		}

		.head-actions :global(.field) {
			width: 100%;
		}

		.head-actions :global(.trigger) {
			width: 100%;
		}
	}
</style>
