<script lang="ts">
	import {
		DEFAULT_SUBSCRIPTION_URLTEST,
		type Subscription,
		type SubscriptionGroup,
		type SubscriptionMode,
	} from '$lib/types';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { resolveGroupPreview } from '$lib/utils/subscriptionGroupPreview';
	import { Button, Modal } from '$lib/components/ui';

	interface Props {
		open: boolean;
		/** null = создание новой группы; иначе редактирование. */
		group: SubscriptionGroup | null;
		subscriptions: Subscription[];
		onclose: () => void;
		onsaved: () => void;
	}
	let { open, group, subscriptions, onclose, onsaved }: Props = $props();

	let label = $state('');
	let tag = $state('');
	let mode = $state<SubscriptionMode>('urltest');
	let utUrl = $state(DEFAULT_SUBSCRIPTION_URLTEST.url);
	let utIntervalSec = $state(DEFAULT_SUBSCRIPTION_URLTEST.intervalSec);
	let utToleranceMs = $state(DEFAULT_SUBSCRIPTION_URLTEST.toleranceMs);
	let selectedIds = $state<string[]>([]);
	let filterInclude = $state('');
	let filterExclude = $state('');
	let enabled = $state(true);
	let saving = $state(false);
	let error = $state('');

	// Сброс формы при каждом открытии (create — пустая, edit — из группы).
	$effect(() => {
		if (!open) return;
		label = group?.label ?? '';
		tag = '';
		mode = group?.mode ?? 'urltest';
		utUrl = group?.urlTest?.url ?? DEFAULT_SUBSCRIPTION_URLTEST.url;
		utIntervalSec = group?.urlTest?.intervalSec ?? DEFAULT_SUBSCRIPTION_URLTEST.intervalSec;
		utToleranceMs = group?.urlTest?.toleranceMs ?? DEFAULT_SUBSCRIPTION_URLTEST.toleranceMs;
		selectedIds = [...(group?.useSubscriptionIds ?? [])];
		filterInclude = group?.filterInclude ?? '';
		filterExclude = group?.filterExclude ?? '';
		enabled = group?.enabled ?? true;
		error = '';
	});

	function toggleSub(id: string): void {
		if (selectedIds.includes(id)) {
			selectedIds = selectedIds.filter((x) => x !== id);
		} else {
			selectedIds = [...selectedIds, id];
		}
	}

	// Живое превью «Попадёт серверов: N» — советное, считается на клиенте
	// из загруженных members; серверная проверка (Go RE2) авторитетна.
	const preview = $derived(
		resolveGroupPreview(subscriptions, selectedIds, filterInclude.trim(), filterExclude.trim()),
	);

	// #572: клиентская проверка формата тега; авторитетна серверная
	// (там же коллизии с другими тегами).
	const GROUP_TAG_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$/;
	const tagValid = $derived.by(() => {
		const t = tag.trim();
		return t === '' || (GROUP_TAG_RE.test(t) && !t.startsWith('sub-'));
	});

	const canSave = $derived(label.trim().length > 0 && tagValid && !saving);

	async function save(): Promise<void> {
		if (!canSave) return;
		saving = true;
		error = '';
		try {
			const payload = {
				label: label.trim(),
				mode,
				urlTest:
					mode === 'urltest'
						? { url: utUrl, intervalSec: utIntervalSec, toleranceMs: utToleranceMs }
						: undefined,
				useSubscriptionIds: selectedIds,
				filterInclude: filterInclude.trim(),
				filterExclude: filterExclude.trim(),
				enabled,
			};
			if (group) {
				await api.updateSubscriptionGroup(group.id, payload);
				notifications.success('Группа обновлена');
			} else {
				await api.createSubscriptionGroup({ ...payload, tag: tag.trim() || undefined });
				notifications.success('Группа создана');
			}
			onsaved();
			onclose();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Не удалось сохранить группу';
		} finally {
			saving = false;
		}
	}
</script>

<Modal
	{open}
	title={group ? 'Изменить группу' : 'Создать сводную группу'}
	size="lg"
	onclose={() => {
		if (saving) return;
		onclose();
	}}
>
	<form
		class="group-form"
		onsubmit={(e) => {
			e.preventDefault();
			void save();
		}}
	>
		<label class="row">
			<span class="lbl">Название</span>
			<input class="inp" bind:value={label} placeholder="Все европейские" required />
		</label>

		{#if group}
			<label class="row">
				<span class="lbl">Тег outbound (sing-box)</span>
				<input class="inp mono" value={group.tag} readonly disabled />
			</label>
		{:else}
			<label class="row">
				<span class="lbl">Тег outbound (sing-box)</span>
				<input
					class="inp mono"
					bind:value={tag}
					placeholder="авто (agg-xxxxxxxx)"
					autocomplete="off"
					spellcheck="false"
				/>
				{#if !tagValid}
					<span class="err">Латиница, цифры и ._-, до 32 символов, первый символ — буква или цифра, без префикса «sub-»</span>
				{/if}
				<div class="hint">
					Имя outbound'а в конфиге sing-box и правилах маршрутизации. После создания не меняется.
				</div>
			</label>
		{/if}

		<div class="block">
			<span class="lbl">Режим выбора сервера</span>
			<div class="mode-grid" role="radiogroup" aria-label="Режим выбора сервера">
				<button
					type="button"
					role="radio"
					aria-checked={mode === 'urltest'}
					class="mode-card"
					class:selected={mode === 'urltest'}
					onclick={() => (mode = 'urltest')}
				>
					<div class="mode-title">Автовыбор по скорости</div>
					<div class="mode-desc">Sing-box держит самый быстрый сервер из всех подписок.</div>
				</button>
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

		<div class="block">
			<span class="lbl">Подписки</span>
			{#if subscriptions.length === 0}
				<div class="empty-subs">Нет подписок — сначала добавьте хотя бы одну.</div>
			{:else}
				<div class="sub-list">
					{#each subscriptions as sub (sub.id)}
						<!-- Выключенная подписка остаётся выбираемой: сервер хранит
						     ссылку и подхватит её членов после включения, но сейчас
						     она в группу ничего не даёт — приглушаем и бейджим. -->
						<label class="sub-item" class:sub-item-off={!sub.enabled}>
							<input
								type="checkbox"
								checked={selectedIds.includes(sub.id)}
								onchange={() => toggleSub(sub.id)}
							/>
							<span class="sub-label">{sub.label || sub.url || sub.id}</span>
							{#if !sub.enabled}<span class="sub-off-badge">отключена</span>{/if}
							<span class="sub-count">{sub.members?.length ?? 0} серв.</span>
						</label>
					{/each}
				</div>
			{/if}
		</div>

		<div class="block">
			<span class="lbl">Фильтр серверов</span>
			<label class="row">
				<span class="lbl-sm">Включать только (regex)</span>
				<input
					class="inp mono"
					bind:value={filterInclude}
					placeholder="(?i)(DE|NL|🇩🇪)"
					autocomplete="off"
					spellcheck="false"
				/>
			</label>
			<label class="row">
				<span class="lbl-sm">Исключать (regex)</span>
				<input
					class="inp mono"
					bind:value={filterExclude}
					placeholder="(?i)(🇷🇺|Россия|RU|BRIDGE|LTE)"
					autocomplete="off"
					spellcheck="false"
				/>
			</label>
			<div class="hint">
				Матчится по имени сервера. Синтаксис Go RE2: lookahead не поддерживается —
				используйте поле «Исключать». Пример:
				<code class="mono">(?i)(🇷🇺|Россия|RU|BRIDGE|LTE)</code>.
				Проверка выражения выполняется на сервере.
			</div>
			<div class="preview" class:preview-empty={preview.count === 0}>
				Попадёт серверов: <strong>{preview.count}</strong>
				{#if preview.lookaroundInclude || preview.lookaroundExclude}
					<span class="preview-warn"
						>· Go RE2 не поддерживает lookahead/lookbehind — используйте поле «Исключать»</span
					>
				{:else if preview.invalidInclude || preview.invalidExclude}
					<span class="preview-warn">· выражение не разобрано, оценка без фильтра</span>
				{/if}
			</div>
		</div>

		{#if error}<div class="err">{error}</div>{/if}
	</form>
	{#snippet actions()}
		<Button variant="ghost" disabled={saving} onclick={onclose}>Отмена</Button>
		<Button variant="primary" disabled={!canSave} loading={saving} onclick={save}>
			{saving ? 'Сохраняем...' : group ? 'Сохранить' : 'Создать'}
		</Button>
	{/snippet}
</Modal>

<style>
	.group-form {
		display: flex;
		flex-direction: column;
		gap: 0.9rem;
	}
	.block {
		display: flex;
		flex-direction: column;
		gap: 0.45rem;
	}
	.row {
		display: flex;
		flex-direction: column;
		gap: 0.3rem;
	}
	.lbl {
		font-size: 0.8rem;
		font-weight: 600;
		color: var(--color-text-secondary);
	}
	.lbl-sm {
		font-size: 0.78rem;
		color: var(--color-text-secondary);
	}
	.inp {
		font: inherit;
		font-size: 0.82rem;
		padding: 0.4375rem 0.625rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm, 4px);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		width: 100%;
		box-sizing: border-box;
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
	}
	.mode-card:hover {
		border-color: var(--color-text-muted);
	}
	.mode-card.selected {
		border-color: var(--color-primary, #3b82f6);
		background: rgba(59, 130, 246, 0.06);
	}
	.mode-title {
		font-weight: 600;
		font-size: 0.82rem;
	}
	.mode-desc {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		line-height: 1.45;
	}
	.urltest-block {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		padding: 0.6rem 0.8rem;
		background: var(--color-bg-primary);
		border: 1px dashed var(--color-border);
		border-radius: 4px;
	}
	.ut-row {
		display: flex;
		gap: 0.6rem;
	}
	.ut-col {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 0.3rem;
	}
	.sub-list {
		display: flex;
		flex-direction: column;
		gap: 0.3rem;
		max-height: 200px;
		overflow-y: auto;
		padding: 0.4rem;
		border: 1px solid var(--color-border);
		border-radius: 6px;
	}
	.sub-item {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		padding: 0.35rem 0.5rem;
		border-radius: 4px;
		cursor: pointer;
		font-size: 0.82rem;
	}
	.sub-item:hover {
		background: var(--color-bg-tertiary);
	}
	.sub-label {
		flex: 1 1 auto;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		color: var(--color-text-primary);
	}
	.sub-count {
		flex-shrink: 0;
		font-size: 0.72rem;
		color: var(--color-text-muted);
	}
	.sub-item-off .sub-label {
		color: var(--color-text-muted);
	}
	.sub-off-badge {
		flex-shrink: 0;
		font-size: 0.65rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		padding: 0.1rem 0.4rem;
		border-radius: 999px;
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
	}
	.empty-subs {
		font-size: 0.8rem;
		color: var(--color-text-muted);
		padding: 0.5rem;
		border: 1px dashed var(--color-border);
		border-radius: 6px;
	}
	.hint {
		font-size: 0.75rem;
		line-height: 1.5;
		color: var(--color-text-muted);
	}
	.hint code {
		font-size: 11px;
		padding: 0 3px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: 3px;
	}
	.preview {
		font-size: 0.8rem;
		color: var(--color-text-primary);
		padding: 0.45rem 0.6rem;
		background: var(--color-accent-tint, rgba(59, 130, 246, 0.08));
		border-radius: 6px;
	}
	.preview-empty {
		color: #d29922;
	}
	.preview-warn {
		color: var(--color-text-muted);
	}
	.err {
		color: #f85149;
		font-size: 0.82rem;
	}
	.mono {
		font-family: var(--font-mono, ui-monospace, monospace);
	}
	@media (max-width: 600px) {
		.mode-grid {
			grid-template-columns: 1fr;
		}
		.ut-row {
			flex-direction: column;
		}
	}
</style>
