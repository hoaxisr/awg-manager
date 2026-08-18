<script lang="ts">
	import {
		DEFAULT_SUBSCRIPTION_URLTEST,
		type Subscription,
		type SubscriptionMode,
	} from '$lib/types';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { goto } from '$app/navigation';
	import HeadersTextarea from './HeadersTextarea.svelte';
	import { parseHeadersText, serializeHeaders } from './headersParser';
	import { Save, Trash2 } from 'lucide-svelte';
	import { Button, Dropdown, Modal, Toggle } from '$lib/components/ui';
	import { untrack } from 'svelte';
	import { showOutboundReferencedError } from '$lib/utils/outboundReferenced';
	import { softCompileGoRegex } from '$lib/utils/subscriptionGroupPreview';
	import { BindInterfacePicker } from '$lib/components/singbox';

	interface Props {
		subscription: Subscription;
		onUpdated: () => void;
		/** Только поле enabled — без полной перезагрузки подписки. */
		onEnabledChanged?: (enabled: boolean) => void;
	}
	let { subscription, onUpdated, onEnabledChanged }: Props = $props();

	let label = $state(untrack(() => subscription.label));
	let url = $state(untrack(() => subscription.url));
	let headersText = $state(untrack(() => serializeHeaders(subscription.headers)));
	let refreshHoursStr = $state(untrack(() => String(subscription.refreshHours)));
	let refreshHours = $state(untrack(() => subscription.refreshHours));
	let enabled = $state(untrack(() => subscription.enabled));
	let mode = $state<SubscriptionMode>(untrack(() => subscription.mode ?? 'selector'));
	let utUrl = $state(
		untrack(() => subscription.urlTest?.url ?? DEFAULT_SUBSCRIPTION_URLTEST.url),
	);
	let utIntervalSec = $state(
		untrack(() => subscription.urlTest?.intervalSec ?? DEFAULT_SUBSCRIPTION_URLTEST.intervalSec),
	);
	let utToleranceMs = $state(
		untrack(() => subscription.urlTest?.toleranceMs ?? DEFAULT_SUBSCRIPTION_URLTEST.toleranceMs),
	);
	let filterInclude = $state(untrack(() => subscription.filterInclude ?? ''));
	let filterExclude = $state(untrack(() => subscription.filterExclude ?? ''));
	let bindInterface = $state(untrack(() => subscription.bindInterface ?? ''));
	let saving = $state(false);
	let togglingEnabled = $state(false);
	let confirmDelete = $state(false);
	let deleting = $state(false);

	$effect(() => {
		label = subscription.label;
		url = subscription.url;
		headersText = serializeHeaders(subscription.headers);
		refreshHoursStr = String(subscription.refreshHours);
		refreshHours = subscription.refreshHours;
		enabled = subscription.enabled;
		mode = subscription.mode ?? 'selector';
		utUrl = subscription.urlTest?.url ?? DEFAULT_SUBSCRIPTION_URLTEST.url;
		utIntervalSec =
			subscription.urlTest?.intervalSec ?? DEFAULT_SUBSCRIPTION_URLTEST.intervalSec;
		utToleranceMs =
			subscription.urlTest?.toleranceMs ?? DEFAULT_SUBSCRIPTION_URLTEST.toleranceMs;
		filterInclude = subscription.filterInclude ?? '';
		filterExclude = subscription.filterExclude ?? '';
		bindInterface = subscription.bindInterface ?? '';
	});

	// Мягкая клиентская проверка regex — НЕ авторитетна; финальная валидация
	// на сервере. softCompileGoRegex понимает ведущий (?i) (JS RegExp его не
	// принимает — плейсхолдер поля не должен подсвечиваться как ошибка) и
	// адресно ловит lookahead/lookbehind, которые JS принял бы, а Go RE2 — нет.
	function softRegexWarning(pattern: string): string {
		const res = softCompileGoRegex(pattern);
		if (res.lookaround) {
			return 'Go RE2 не поддерживает lookahead/lookbehind — используйте поле «Исключать»';
		}
		if (res.invalid) {
			return 'Похоже на некорректное выражение (окончательно проверит сервер)';
		}
		return '';
	}
	const includeWarning = $derived(softRegexWarning(filterInclude));
	const excludeWarning = $derived(softRegexWarning(filterExclude));

	$effect(() => {
		refreshHours = parseInt(refreshHoursStr, 10) || 0;
	});

	const refreshOptions = [
		{ value: '0', label: 'Только вручную' },
		{ value: '1', label: 'Каждый час' },
		{ value: '6', label: 'Каждые 6 часов' },
		{ value: '12', label: 'Каждые 12 часов' },
		{ value: '24', label: 'Раз в сутки' },
		{ value: '168', label: 'Раз в неделю' },
	];

	async function toggleEnabled(next: boolean): Promise<void> {
		if (togglingEnabled) return;
		togglingEnabled = true;
		try {
			const saved = await api.updateSubscription(subscription.id, { enabled: next });
			enabled = saved.enabled;
			onEnabledChanged?.(saved.enabled);
			notifications.success(saved.enabled ? 'Подписка включена' : 'Подписка выключена');
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось изменить состояние');
		} finally {
			togglingEnabled = false;
		}
	}

	async function save(): Promise<void> {
		saving = true;
		try {
			const patch: Parameters<typeof api.updateSubscription>[1] = {
				label,
				enabled,
				mode,
				urlTest:
					mode === 'urltest'
						? { url: utUrl, intervalSec: utIntervalSec, toleranceMs: utToleranceMs }
						: undefined,
				filterInclude: filterInclude.trim(),
				filterExclude: filterExclude.trim(),
				bindInterface: bindInterface.trim(),
			};
			if (!subscription.isInline) {
				patch.url = url;
				patch.headers = parseHeadersText(headersText);
				patch.refreshHours = refreshHours;
			}
			await api.updateSubscription(subscription.id, patch);
			onUpdated();
		} catch (e) {
			// Сервер отвечает 400 на невалидный regex-фильтр (RE2) —
			// показываем текст ошибки, настройки не сохранены.
			notifications.error(e instanceof Error ? e.message : 'Не удалось сохранить настройки');
		} finally {
			saving = false;
		}
	}

	async function doDelete(): Promise<void> {
		deleting = true;
		try {
			await api.deleteSubscription(subscription.id);
			goto('/?tab=subscriptions');
		} catch (e) {
			const name = subscription.label || subscription.selectorTag || subscription.id;
			if (!showOutboundReferencedError(e, name, 'Подписка')) {
				notifications.error(e instanceof Error ? e.message : 'Не удалось удалить подписку');
			}
		} finally {
			deleting = false;
		}
	}
</script>

{#snippet saveIcon()}
	<Save size={14} strokeWidth={2} aria-hidden="true" />
{/snippet}

{#snippet deleteIcon()}
	<Trash2 size={14} strokeWidth={2} aria-hidden="true" />
{/snippet}

<div class="settings-toolbar">
	<Button variant="primary" disabled={saving} loading={saving} iconBefore={saveIcon} onclick={save}>
		{saving ? 'Сохраняем...' : 'Сохранить'}
	</Button>
	<Button variant="danger" iconBefore={deleteIcon} onclick={() => (confirmDelete = true)}>
		Удалить подписку
	</Button>
</div>

<form
	class="form-grid"
	onsubmit={(e) => {
		e.preventDefault();
		save();
	}}
>
	<section class="col control-col">
		<div class="enabled-card" class:off={!enabled}>
			<div class="enabled-row">
				<Toggle
					checked={enabled}
					controlled
					variant="flip"
					loading={togglingEnabled}
					onchange={toggleEnabled}
				/>
				<div class="enabled-text">
					<span class="enabled-title">Включена</span>
					<span class="enabled-hint">
						{#if enabled}
							Подписка включена и участвует в маршрутизации
						{:else}
							Подписка выключена — не используется в маршрутизации
						{/if}
					</span>
				</div>
			</div>
		</div>

		<div class="mode-section">
			<h3 class="col-title">Режим выбора сервера</h3>
			<div class="mode-grid" role="radiogroup" aria-label="Режим выбора сервера">
				<button
					type="button"
					role="radio"
					aria-checked={mode === 'selector'}
					class="mode-card"
					class:selected={mode === 'selector'}
					onclick={() => (mode = 'selector')}
				>
					<div class="mode-title">Ручной выбор</div>
					<div class="mode-desc">Сервер переключается вручную из списка.</div>
					{#if mode === 'selector'}
						<span class="mode-check" aria-hidden="true">
							<svg viewBox="0 0 24 24"><polyline points="20 6 9 17 4 12" /></svg>
						</span>
					{/if}
				</button>
				<button
					type="button"
					role="radio"
					aria-checked={mode === 'urltest'}
					class="mode-card"
					class:selected={mode === 'urltest'}
					onclick={() => (mode = 'urltest')}
				>
					<div class="mode-title">Автовыбор по скорости</div>
					<div class="mode-desc">
						Sing-box сам пингует серверы и держит самый быстрый.
					</div>
					{#if mode === 'urltest'}
						<span class="mode-check" aria-hidden="true">
							<svg viewBox="0 0 24 24"><polyline points="20 6 9 17 4 12" /></svg>
						</span>
					{/if}
				</button>
			</div>
			{#if mode === 'urltest'}
				<div class="urltest-block">
					<label class="row">
						<span class="lbl">URL для проверки</span>
						<input
							class="inp"
							type="url"
							bind:value={utUrl}
							placeholder={DEFAULT_SUBSCRIPTION_URLTEST.url}
						/>
					</label>
					<div class="ut-row">
						<label class="ut-col">
							<span class="lbl">Интервал, сек</span>
							<input class="inp" type="number" min="10" max="3600" bind:value={utIntervalSec} />
						</label>
						<label class="ut-col">
							<span class="lbl">Допуск, мс</span>
							<input class="inp" type="number" min="0" max="2000" bind:value={utToleranceMs} />
						</label>
					</div>
				</div>
			{/if}
		</div>

		<div class="bind-section">
			<h3 class="col-title">Исходящий интерфейс</h3>
			<BindInterfacePicker
				bind:value={bindInterface}
				hint="Весь трафик серверов этой подписки будет dial'иться через выбранный uplink. Удобно для модема, отдельного WAN или Wi‑Fi."
			/>
		</div>

		<div class="settings-summary" aria-label="Данные подписки">
			<div class="summary-head">
				<h3 class="col-title">Данные подписки</h3>
				<span class="summary-state" class:enabled={enabled}>
					{enabled ? 'активна' : 'выключена'}
				</span>
			</div>
			<div class="summary-grid">
				<div class="summary-item">
					<span class="summary-value">{subscription.members.length}</span>
					<span class="summary-label">серверов</span>
				</div>
				<div class="summary-item">
					<span class="summary-value">{subscription.isInline ? 'ручной список' : 'URL'}</span>
					<span class="summary-label">источник</span>
				</div>
				<div class="summary-item">
					<span class="summary-value">{mode === 'urltest' ? 'автовыбор' : 'ручной выбор'}</span>
					<span class="summary-label">режим</span>
				</div>
				<div class="summary-item">
					<span class="summary-value">
						{subscription.isInline
							? 'не требуется'
							: refreshHours > 0
								? `${refreshHours} ч`
								: 'вручную'}
					</span>
					<span class="summary-label">обновление</span>
				</div>
				<div class="summary-item">
					<span class="summary-value">
						{subscription.isInline
							? '—'
							: parseHeadersText(headersText).length > 0
								? `${parseHeadersText(headersText).length}`
								: 'нет'}
					</span>
					<span class="summary-label">заголовков</span>
				</div>
				<div class="summary-item">
					<span class="summary-value">{mode === 'urltest' ? 'sing-box' : subscription.activeMember || 'не выбран'}</span>
					<span class="summary-label">активный</span>
				</div>
				<div class="summary-item">
					<span class="summary-value mono">Proxy{subscription.proxyIndex}</span>
					<span class="summary-label">ndms proxy</span>
				</div>
				<div class="summary-item">
					<span class="summary-value mono">{subscription.selectorTag}</span>
					<span class="summary-label">selector</span>
				</div>
			</div>
		</div>
	</section>

	<section class="col source-col">
		<h3 class="col-title">Источник</h3>
		<label class="row">
			<span class="lbl">Название</span>
			<input class="inp" bind:value={label} />
		</label>
		{#if subscription.isInline}
			<div class="inline-info">
				<div class="inline-badge">Список вручную</div>
				<div class="inline-summary">
					{subscription.members.length === 1
						? '1 сервер'
						: subscription.members.length < 5
							? `${subscription.members.length} сервера`
							: `${subscription.members.length} серверов`}
					· редактирование во вкладке «Серверы»
				</div>
			</div>
		{:else}
			<label class="row">
				<span class="lbl">URL</span>
				<input class="inp" bind:value={url} />
			</label>
			<HeadersTextarea bind:value={headersText} />
			<Dropdown
				label="Авто-обновление"
				bind:value={refreshHoursStr}
				options={refreshOptions}
				fullWidth
			/>
		{/if}

		<div class="filter-section">
			<h3 class="col-title">Фильтр серверов</h3>
			<label class="row">
				<span class="lbl">Включать только (regex)</span>
				<input
					class="inp mono"
					bind:value={filterInclude}
					placeholder="(?i)(DE|NL|🇩🇪)"
					autocomplete="off"
					spellcheck="false"
				/>
				{#if includeWarning}<span class="filter-warn">{includeWarning}</span>{/if}
			</label>
			<label class="row">
				<span class="lbl">Исключать (regex)</span>
				<input
					class="inp mono"
					bind:value={filterExclude}
					placeholder="(?i)(🇷🇺|Россия|RU|BRIDGE|LTE)"
					autocomplete="off"
					spellcheck="false"
				/>
				{#if excludeWarning}<span class="filter-warn">{excludeWarning}</span>{/if}
			</label>
			<div class="filter-hint">
				Матчится по имени сервера. Синтаксис Go RE2: lookahead
				(<code class="mono">(?!...)</code>) не поддерживается — вместо него
				используйте поле «Исключать». Пример:
				<code class="mono">(?i)(🇷🇺|Россия|RU|BRIDGE|LTE)</code>.
				Проверка выражения выполняется на сервере; скрытые серверы видны во
				вкладке «Серверы» в блоке «Скрыто фильтром».
			</div>
			{#if subscription.isInline}
				<div class="filter-hint filter-hint-inline">
					У inline-подписки изменение фильтра пересобирает список из исходного
					текста: ручные добавления/удаления серверов будут сброшены.
				</div>
			{/if}
		</div>
	</section>

	
</form>

<Modal
	open={confirmDelete}
	title="Удалить подписку?"
	size="md"
	onclose={() => {
		if (deleting) return;
		confirmDelete = false;
	}}
>
	<p>
		Подписка <strong>{subscription.label || subscription.url}</strong> будет
		удалена вместе с её sing-box outbound'ами и NDMS Proxy
		<code class="mono">Proxy{subscription.proxyIndex}</code>.
	</p>
	{#snippet actions()}
		<Button variant="ghost" disabled={deleting} onclick={() => (confirmDelete = false)}>
			Отмена
		</Button>
		<Button variant="danger" disabled={deleting} loading={deleting} onclick={doDelete}>
			{deleting ? 'Удаляем...' : 'Удалить'}
		</Button>
	{/snippet}
</Modal>

<style>
	.settings-toolbar {
		display: flex;
		justify-content: flex-end;
		gap: 0.5rem;
		margin-bottom: 1rem;
	}

	@media (max-width: 640px) {
		.settings-toolbar {
			display: grid;
			grid-template-columns: repeat(2, minmax(0, 1fr));
			align-items: stretch;
			width: 100%;
		}

		.settings-toolbar :global(.btn) {
			width: 100%;
			justify-content: center;
			text-align: center;
		}

		.settings-toolbar :global(.btn .label) {
			justify-content: center;
			text-align: center;
		}
	}

	.enabled-card {
		padding: 12px 14px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius, 8px);
		transition:
			border-color var(--t-fast, 120ms) ease,
			background var(--t-fast, 120ms) ease;
	}
	.enabled-card.off {
		border-color: color-mix(in srgb, var(--color-text-muted) 35%, var(--color-border));
	}
	.enabled-row {
		display: flex;
		align-items: center;
		gap: 10px;
		min-width: 0;
	}
	.enabled-text {
		flex: 1;
		display: flex;
		flex-direction: column;
		gap: 3px;
		min-width: 0;
	}
	.enabled-title {
		font-size: var(--sub-title);
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.enabled-hint {
		font-size: var(--sub-meta);
		line-height: 1.45;
		color: var(--color-text-muted);
	}

	/* Typography: 14px titles · 13px labels/inputs/body · 12px hints/meta */
	.form-grid {
		--sub-title: 14px;
		--sub-body: 13px;
		--sub-meta: 12px;
		display: grid;
		grid-template-columns: 1fr;
		gap: 1.25rem;
	}
	@media (min-width: 900px) {
		.form-grid { grid-template-columns: 1fr 1fr; }
	}

	.col {
		display: flex;
		flex-direction: column;
		gap: 0.7rem;
		padding: 1rem 1.1rem;
		background: var(--color-bg-secondary, var(--color-bg-primary));
		border: 1px solid var(--color-border);
		border-radius: 8px;
	}
	.control-col {
		gap: 1rem;
	}
	.mode-section {
		display: flex;
		flex-direction: column;
		gap: 0.7rem;
	}
	.bind-section {
		display: flex;
		flex-direction: column;
		gap: 0.7rem;
		margin-top: 0.5rem;
		padding-top: 0.7rem;
		border-top: 1px solid var(--color-border);
	}
	.settings-summary {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
		margin-top: auto;
		padding-top: 0.9rem;
		border-top: 1px solid var(--color-border);
	}
	.summary-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
	}
	.summary-head .col-title {
		margin: 0;
	}
	.summary-state {
		display: inline-flex;
		align-items: center;
		padding: 0.15rem 0.5rem;
		border-radius: 999px;
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
		font-size: var(--sub-meta);
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}
	.summary-state.enabled {
		background: color-mix(in srgb, var(--color-success, #22c55e) 16%, transparent);
		color: var(--color-success, #22c55e);
	}
	.summary-grid {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 0.5rem;
	}
	.summary-item {
		min-width: 0;
		padding: 0.55rem 0.65rem;
		border: 1px solid var(--color-border);
		border-radius: 6px;
		background: var(--color-bg-primary);
	}
	.summary-value,
	.summary-label {
		display: block;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.summary-value {
		font-size: var(--sub-body);
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.summary-label {
		margin-top: 0.22rem;
		font-size: var(--sub-meta);
		font-weight: 600;
		letter-spacing: 0.04em;
		text-transform: uppercase;
		color: var(--color-text-muted);
	}
	.col-title {
		margin: 0 0 0.3rem;
		font-size: var(--sub-meta);
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-text-muted);
		font-weight: 600;
	}

	.row { display: flex; flex-direction: column; gap: 0.3rem; }
	.lbl {
		font-size: var(--sub-body);
		font-weight: 500;
		color: var(--color-text-secondary);
	}
	.inp {
		font: inherit;
		font-size: var(--sub-body);
		line-height: 1.4;
		padding: 0.4375rem 0.625rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm, 4px);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		width: 100%;
		box-sizing: border-box;
		transition: border-color var(--t-fast, 120ms) ease;
	}
	.inp:focus {
		outline: none;
		border-color: var(--color-accent);
	}
	.mode-grid {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 0.5rem;
	}
	.mode-card {
		position: relative;
		text-align: left;
		padding: 0.6rem 0.75rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
		color: var(--color-text-primary);
		cursor: pointer;
		font: inherit;
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
		transition: border-color 120ms, background 120ms;
	}
	.mode-card:hover { border-color: var(--color-text-muted); }
	.mode-card.selected {
		border-color: var(--color-primary, #3b82f6);
		background: rgba(59, 130, 246, 0.06);
	}
	.mode-card:focus-visible {
		outline: 2px solid var(--color-primary, #3b82f6);
		outline-offset: 2px;
	}
	.mode-title {
		font-weight: 600;
		font-size: var(--sub-body);
		color: var(--color-text-primary);
	}
	.mode-desc {
		font-size: var(--sub-meta);
		color: var(--color-text-muted);
		line-height: 1.45;
	}
	.mode-check {
		position: absolute;
		top: 0.5rem;
		right: 0.5rem;
		width: 14px;
		height: 14px;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		color: var(--color-primary, #3b82f6);
	}
	.mode-check svg { width: 12px; height: 12px; fill: none; stroke: currentColor; stroke-width: 3; }
	.urltest-block {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		padding: 0.6rem 0.8rem;
		background: var(--color-bg-primary);
		border: 1px dashed var(--color-border);
		border-radius: 4px;
	}
	.ut-row { display: flex; gap: 0.6rem; }
	.ut-col { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 0.3rem; }

	.inline-info {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
		padding: 0.6rem 0.8rem;
		background: var(--color-bg-primary);
		border: 1px dashed var(--color-border);
		border-radius: 4px;
	}
	.inline-badge {
		display: inline-flex;
		align-self: flex-start;
		padding: 0.15rem 0.5rem;
		background: var(--color-accent, #3b82f6);
		color: var(--color-accent-contrast, #ffffff);
		border-radius: 3px;
		font-size: var(--sub-meta);
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}
	.inline-summary {
		font-size: var(--sub-body);
		line-height: 1.45;
		color: var(--color-text-muted);
	}

	.filter-section {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		margin-top: 0.5rem;
		padding-top: 0.7rem;
		border-top: 1px solid var(--color-border);
	}
	.filter-warn {
		font-size: var(--sub-meta);
		color: #d29922;
	}
	.filter-hint {
		font-size: var(--sub-meta);
		line-height: 1.5;
		color: var(--color-text-muted);
	}
	.filter-hint code {
		font-size: 11px;
		padding: 0 3px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: 3px;
	}
	.filter-hint-inline {
		color: #d29922;
	}

	.mono { font-family: var(--font-mono, ui-monospace, monospace); }

	@media (max-width: 600px) {
		.mode-grid { grid-template-columns: 1fr; }
		.ut-row { flex-direction: column; }
	}
</style>
