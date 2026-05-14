<script lang="ts">
	import { goto } from '$app/navigation';
	import { Modal, Button, Dropdown } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { singboxStatus, singboxTunnels } from '$lib/stores/singbox';
	import {
		DEFAULT_SUBSCRIPTION_URLTEST,
		type SubscriptionMode,
	} from '$lib/types';
	import HeadersTextarea from './HeadersTextarea.svelte';
	import ShareLinksTextarea from './ShareLinksTextarea.svelte';
	import { DEFAULT_PRESET, parseHeadersText } from './headersParser';
	import {
		mergePastedShareList,
		normalizeSpaceSeparatedShareLinks,
	} from '$lib/utils/shareLinkListInput';

	type WizardKind = 'single' | 'inline' | 'url';

	interface Props {
		open: boolean;
		/** Preselect a step-2 form. When unset (or 'choose') the wizard
		 * opens on step 1 (the three cards). Callers from contextual
		 * "+ Add" buttons usually pass a preselect; emptystate cards
		 * can also pass a preselect to skip step 1. */
		preselect?: WizardKind | 'choose';
		onclose?: () => void;
	}

	let { open = $bindable(false), preselect = 'choose', onclose }: Props = $props();

	let kind = $state<WizardKind | 'choose'>('choose');
	let submitting = $state(false);
	let error = $state('');

	// "Один сервер" state — paste of N share-links, each becomes its
	// own sing-box tunnel via /singbox/import-links.
	let singleLinks = $state('');
	let singleResult = $state<{ imported: number; errors: string[] } | null>(null);

	// "Группа серверов" / "Подписка" — shared subscription create state.
	let label = $state('');
	let url = $state('');
	let inlineText = $state('');
	let headersText = $state(DEFAULT_PRESET);
	let refreshHoursStr = $state('24');
	let refreshHours = $state(24);
	let enabled = $state(true);
	let mode = $state<SubscriptionMode>('selector');
	let utUrl = $state(DEFAULT_SUBSCRIPTION_URLTEST.url);
	let utIntervalSec = $state(DEFAULT_SUBSCRIPTION_URLTEST.intervalSec);
	let utToleranceMs = $state(DEFAULT_SUBSCRIPTION_URLTEST.toleranceMs);

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

	const singboxInstalled = $derived($singboxStatus.data?.installed ?? false);
	// Compare every form field against its reset() default. The previous
	// `kind !== 'choose'` heuristic mis-fired the moment the user picked
	// a step (or arrived with a preselect), claiming dirty without any
	// input. Now dirty reflects actual edits.
	const isDirty = $derived.by(() => {
		if (kind === 'choose') return false;
		if (kind === 'single') return singleLinks.trim() !== '';
		// 'inline' and 'url' share the subscription form below.
		return (
			label.trim() !== '' ||
			url.trim() !== '' ||
			inlineText.trim() !== '' ||
			headersText !== DEFAULT_PRESET ||
			refreshHoursStr !== '24' ||
			enabled !== true ||
			mode !== 'selector' ||
			utUrl !== DEFAULT_SUBSCRIPTION_URLTEST.url ||
			utIntervalSec !== DEFAULT_SUBSCRIPTION_URLTEST.intervalSec ||
			utToleranceMs !== DEFAULT_SUBSCRIPTION_URLTEST.toleranceMs
		);
	});

	$effect(() => {
		if (open) {
			kind = preselect;
		}
	});

	function reset(): void {
		kind = 'choose';
		singleLinks = '';
		singleResult = null;
		label = '';
		url = '';
		inlineText = '';
		headersText = DEFAULT_PRESET;
		refreshHoursStr = '24';
		refreshHours = 24;
		enabled = true;
		mode = 'selector';
		utUrl = DEFAULT_SUBSCRIPTION_URLTEST.url;
		utIntervalSec = DEFAULT_SUBSCRIPTION_URLTEST.intervalSec;
		utToleranceMs = DEFAULT_SUBSCRIPTION_URLTEST.toleranceMs;
		error = '';
	}

	function close(): void {
		if (submitting) return;
		open = false;
		reset();
		onclose?.();
	}

	function backToChoose(): void {
		if (submitting) return;
		kind = 'choose';
		error = '';
	}

	function onShareListPaste(
		e: ClipboardEvent & { currentTarget: HTMLTextAreaElement },
		get: () => string,
		set: (v: string) => void,
	): void {
		const data = e.clipboardData?.getData('text/plain');
		if (data == null) return;
		const normalized = normalizeSpaceSeparatedShareLinks(data);
		if (normalized === data) return;
		e.preventDefault();
		const ta = e.currentTarget;
		const { next, caret } = mergePastedShareList(
			get(),
			ta.selectionStart ?? 0,
			ta.selectionEnd ?? 0,
			data,
		);
		set(next);
		queueMicrotask(() => {
			ta.selectionStart = ta.selectionEnd = caret;
		});
	}

	const titleByKind: Record<WizardKind | 'choose', string> = {
		choose: 'Добавить',
		single: 'Один сервер',
		inline: 'Группа серверов',
		url: 'Подписка по URL',
	};

	async function submitSingle(): Promise<void> {
		singleLinks = normalizeSpaceSeparatedShareLinks(singleLinks);
		if (!singleLinks.trim() || submitting) return;
		submitting = true;
		error = '';
		singleResult = null;
		try {
			const res = await api.singboxImportLinks(singleLinks);
			singboxTunnels.applyMutationResponse(res.tunnels);
			singleResult = {
				imported: res.imported?.length ?? 0,
				errors: (res.errors ?? []).map((e) => e.error),
			};
			if ((res.imported?.length ?? 0) > 0) {
				open = false;
				reset();
				goto('/?tab=singbox');
			}
		} catch (e) {
			error = e instanceof Error ? e.message : 'Не удалось импортировать';
		} finally {
			submitting = false;
		}
	}

	async function submitSubscription(): Promise<void> {
		if (submitting) return;
		const isInline = kind === 'inline';
		if (isInline) {
			inlineText = normalizeSpaceSeparatedShareLinks(inlineText);
		}
		if (isInline && !inlineText.trim()) {
			error = 'Вставьте хотя бы одну ссылку';
			return;
		}
		if (!isInline && !url.trim()) {
			error = 'Укажите URL подписки';
			return;
		}
		submitting = true;
		error = '';
		try {
			const sub = await api.createSubscription({
				label,
				url: isInline ? undefined : url,
				inline: isInline ? inlineText : undefined,
				headers: isInline ? [] : parseHeadersText(headersText),
				refreshHours: isInline ? 0 : refreshHours,
				enabled,
				mode,
				urlTest:
					mode === 'urltest'
						? { url: utUrl, intervalSec: utIntervalSec, toleranceMs: utToleranceMs }
						: undefined,
			});
			open = false;
			reset();
			goto(`/subscriptions/${sub.id}`);
		} catch (e) {
			error = e instanceof Error ? e.message : 'Не удалось создать';
		} finally {
			submitting = false;
		}
	}
</script>

<Modal {open} title={titleByKind[kind]} size="lg" onclose={close} hasUnsavedChanges={() => isDirty}>
	{#if kind === 'choose'}
		<p class="lead">Что добавить?</p>
		<div class="kind-grid">
			<button type="button" class="kind-card" onclick={() => (kind = 'single')}>
				<svg class="kind-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
					<path d="M10 13a5 5 0 0 0 7.07 0l3-3a5 5 0 0 0-7.07-7.07L11 5" />
					<path d="M14 11a5 5 0 0 0-7.07 0l-3 3a5 5 0 0 0 7.07 7.07L13 19" />
				</svg>
				<div class="kind-title">Один сервер</div>
				<div class="kind-desc">
					Вставь одну или несколько share-link'ов — каждая станет
					отдельным sing-box туннелем со своим Proxy NDMS.
				</div>
			</button>
			<button type="button" class="kind-card" onclick={() => (kind = 'inline')}>
				<svg class="kind-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
					<rect x="3" y="3" width="7" height="7" rx="1" />
					<rect x="14" y="3" width="7" height="7" rx="1" />
					<rect x="3" y="14" width="7" height="7" rx="1" />
					<rect x="14" y="14" width="7" height="7" rx="1" />
				</svg>
				<div class="kind-title">Группа серверов</div>
				<div class="kind-desc">
					Несколько ссылок становятся одной группой с общим Proxy.
					Внутри — ручное переключение или автовыбор по скорости.
				</div>
			</button>
			<button type="button" class="kind-card" onclick={() => (kind = 'url')}>
				<svg class="kind-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
					<circle cx="12" cy="12" r="10" />
					<line x1="2" y1="12" x2="22" y2="12" />
					<path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z" />
				</svg>
				<div class="kind-title">Подписка по URL</div>
				<div class="kind-desc">
					Адрес подписки провайдера. Список серверов обновляется
					автоматически по расписанию.
				</div>
			</button>
		</div>
	{:else if kind === 'single'}
		<form
			class="form"
			onsubmit={(e) => {
				e.preventDefault();
				void submitSingle();
			}}
		>
			<p class="lead">
				Каждая строка — отдельный sing-box туннель со своим Proxy NDMS.
				Поддерживаются <code>vless://</code>, <code>hy2://</code>,
				<code>trojan://</code>, <code>ss://</code>, <code>hysteria2://</code>,
				<code>naive+http://</code>, <code>naive+https://</code>.
				Список через пробел при вставке разбивается на строки автоматически.
			</p>
			{#if !singboxInstalled}
				<div class="warn">
					Sing-box не установлен — установи в настройках перед добавлением туннелей.
				</div>
			{/if}
			<ShareLinksTextarea
				bind:value={singleLinks}
				placeholder={`vless://uuid@host:443?...#Germany\nhysteria2://pass@host:8443#Finland`}
				rows={6}
				disabled={!singboxInstalled || submitting}
				onpaste={(e) => onShareListPaste(e, () => singleLinks, (v) => (singleLinks = v))}
			/>
			{#if error}<div class="err">{error}</div>{/if}
			{#if singleResult && singleResult.errors.length > 0}
				<div class="err">
					<div>Импортировано: {singleResult.imported}, ошибок: {singleResult.errors.length}</div>
					<ul class="err-list">
						{#each singleResult.errors as e}<li>{e}</li>{/each}
					</ul>
				</div>
			{/if}
		</form>
	{:else}
		<form
			class="form"
			onsubmit={(e) => {
				e.preventDefault();
				void submitSubscription();
			}}
		>
			<label class="row">
				<span class="lbl">Название</span>
				<input class="inp" type="text" bind:value={label} placeholder="Provider X" required />
			</label>

			{#if kind === 'url'}
				<label class="row">
					<span class="lbl">URL подписки</span>
					<input
						class="inp"
						type="url"
						bind:value={url}
						placeholder="https://provider.example/sub/abc"
					/>
				</label>
				<div class="row">
					<HeadersTextarea bind:value={headersText} />
				</div>
				<div class="row">
					<Dropdown
						label="Авто-обновление"
						bind:value={refreshHoursStr}
						options={refreshOptions}
						fullWidth
					/>
				</div>
			{:else}
				<label class="row">
					<span class="lbl">Ссылки на серверы (по одной на строку)</span>
					<ShareLinksTextarea
						bind:value={inlineText}
						placeholder={`vless://...\ntrojan://...\nhysteria2://...\nnaive+https://\nss://...`}
						rows={6}
						onpaste={(e) => onShareListPaste(e, () => inlineText, (v) => (inlineText = v))}
					/>
					<span class="hint">
						Поддерживаются share-link'и, Clash YAML и sing-box JSON.
						Список ссылок через пробел при вставке разбивается на строки.
						Авто-обновления нет — список замораживается на момент создания,
						редактируется во вкладке «Серверы».
					</span>
				</label>
			{/if}

			<div class="row">
				<span class="lbl">Режим выбора сервера</span>
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
						<div class="mode-desc">
							Сервер переключается вручную из списка.
						</div>
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
					<div class="row two-col">
						<label class="col">
							<span class="lbl">Интервал, сек</span>
							<input class="inp" type="number" min="10" max="3600" bind:value={utIntervalSec} />
						</label>
						<label class="col">
							<span class="lbl">Допуск, мс</span>
							<input class="inp" type="number" min="0" max="2000" bind:value={utToleranceMs} />
						</label>
					</div>
				</div>
			{/if}

			<label class="row chk">
				<input type="checkbox" bind:checked={enabled} />
				<span>Включить сразу</span>
			</label>
			{#if error}<div class="err">{error}</div>{/if}
		</form>
	{/if}

	{#snippet actions()}
		{#if kind !== 'choose'}
			<Button variant="ghost" onclick={backToChoose} disabled={submitting}>← Назад</Button>
		{/if}
		<Button variant="ghost" onclick={close} disabled={submitting}>Отмена</Button>
		{#if kind === 'single'}
			<Button
				variant="primary"
				onclick={submitSingle}
				disabled={submitting || !singleLinks.trim() || !singboxInstalled}
				loading={submitting}
			>
				{submitting ? 'Импорт...' : 'Импортировать'}
			</Button>
		{:else if kind !== 'choose'}
			<Button
				variant="primary"
				onclick={submitSubscription}
				disabled={submitting}
				loading={submitting}
			>
				{submitting ? 'Создаём...' : 'Создать'}
			</Button>
		{/if}
	{/snippet}
</Modal>

<style>
	.lead { color: var(--color-text-muted); font-size: 0.85rem; line-height: 1.5; margin: 0 0 0.8rem; }
	.lead code {
		background: var(--color-bg-tertiary, var(--color-bg-primary));
		padding: 0 4px;
		border-radius: 3px;
		font-family: var(--font-mono, ui-monospace, monospace);
		font-size: 0.78rem;
	}

	.kind-grid {
		display: grid;
		grid-template-columns: 1fr;
		gap: 0.6rem;
	}
	@media (min-width: 600px) {
		.kind-grid { grid-template-columns: 1fr 1fr 1fr; }
	}
	.kind-card {
		display: flex;
		flex-direction: column;
		gap: 0.4rem;
		padding: 1rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: 8px;
		text-align: left;
		cursor: pointer;
		font: inherit;
		color: var(--color-text-primary);
		transition: border-color 120ms, transform 120ms, background 120ms;
	}
	.kind-card:hover {
		border-color: var(--color-primary, #3b82f6);
		background: rgba(59, 130, 246, 0.04);
		transform: translateY(-1px);
	}
	.kind-card:focus-visible {
		outline: 2px solid var(--color-primary, #3b82f6);
		outline-offset: 2px;
	}
	.kind-icon { width: 28px; height: 28px; color: var(--color-primary, #3b82f6); }
	.kind-title { font-weight: 500; font-size: 0.92rem; }
	.kind-desc { color: var(--color-text-muted); font-size: 0.78rem; line-height: 1.4; }

	.form { display: flex; flex-direction: column; gap: 1rem; }
	.row { display: flex; flex-direction: column; gap: 0.3rem; }
	.row.chk { flex-direction: row; align-items: center; gap: 0.5rem; }
	.row.two-col { flex-direction: row; gap: 0.75rem; }
	.col { flex: 1; display: flex; flex-direction: column; gap: 0.3rem; min-width: 0; }
	.lbl { font-size: 0.85rem; color: var(--color-text-muted); }
	.inp {
		padding: 0.5rem 0.7rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: 4px;
		color: var(--color-text-primary);
	}
	.hint {
		font-size: 0.74rem;
		color: var(--color-text-muted);
		line-height: 1.4;
		margin-top: 0.25rem;
	}
	.err { color: var(--color-error, #f85149); font-size: 0.85rem; }
	.err-list { margin: 0.4rem 0 0; padding-left: 1.2rem; }
	.warn {
		padding: 0.6rem 0.8rem;
		background: rgba(245, 158, 11, 0.08);
		border: 1px solid var(--warning, #d29922);
		border-radius: 4px;
		font-size: 0.82rem;
		color: var(--warning, #d29922);
	}

	.mode-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 0.5rem; }
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
	.mode-title { font-weight: 500; font-size: 0.85rem; }
	.mode-desc { font-size: 0.72rem; color: var(--color-text-muted); line-height: 1.35; }
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
		background: var(--color-bg-secondary, var(--color-bg-primary));
		border: 1px dashed var(--color-border);
		border-radius: 4px;
	}
	@media (max-width: 480px) {
		.mode-grid { grid-template-columns: 1fr; }
		.row.two-col { flex-direction: column; }
	}
</style>
