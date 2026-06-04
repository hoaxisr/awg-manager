<script lang="ts">
	import Modal from '$lib/components/ui/Modal.svelte';
	import { Button, Dropdown, type DropdownOption } from '$lib/components/ui';
	import type { SingboxRouterOutbound, SingboxRouterWANInterface } from '$lib/types';
	import type { OutboundGroup } from './outboundOptions';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import { resolveMemberLabel } from '$lib/utils/memberLabel';
	import { api } from '$lib/api/client';

	// Only urltest and selector are offered for new groups — `loadbalance`
	// was removed in sing-box 1.13+ and FATALs on startup if present. Legacy
	// loadbalance entries that may exist in older 20-router.json files are
	// still tolerated at read time. When the user edits such an entry through
	// this modal, the type narrows to urltest on open — that's a deliberate
	// one-way migration, not an accidental data loss.

	interface Props {
		outbound?: SingboxRouterOutbound;
		outboundOptions: OutboundGroup[];
		onClose: () => void;
		onSave: (o: SingboxRouterOutbound) => Promise<void> | void;
	}
	let { outbound, outboundOptions, onClose, onSave }: Props = $props();

	// svelte-ignore state_referenced_locally
	let type: 'urltest' | 'selector' | 'direct' = $state(
		outbound?.type === 'selector' ? 'selector' : outbound?.type === 'direct' ? 'direct' : 'urltest'
	);
	// svelte-ignore state_referenced_locally
	let tag = $state(outbound?.tag ?? '');
	// svelte-ignore state_referenced_locally
	let members = $state<string[]>([...(outbound?.outbounds ?? [])]);
	// svelte-ignore state_referenced_locally
	let url = $state(outbound?.url ?? 'https://www.gstatic.com/generate_204');
	// svelte-ignore state_referenced_locally
	let interval = $state(outbound?.interval ?? '3m');
	// svelte-ignore state_referenced_locally
	let tolerance = $state(outbound?.tolerance ?? 50);
	// svelte-ignore state_referenced_locally
	let defaultOutbound = $state(outbound?.default ?? '');
	// svelte-ignore state_referenced_locally
	let bindInterface = $state(outbound?.bind_interface ?? '');
	let bindables = $state<SingboxRouterWANInterface[]>([]);
	let bindablesLoading = $state(true);
	$effect(() => {
		void api.singboxRouterListBindableInterfaces()
			.then((l) => { bindables = l; })
			.catch(() => { bindables = []; })
			.finally(() => { bindablesLoading = false; });
	});
	const bindableOptions = $derived<DropdownOption[]>(
		bindables.map((i) => ({ value: i.name, label: `${i.label} · ${i.name}${i.up ? '' : ' (down)'}` }))
	);

	let busy = $state(false);
	let error = $state('');
	let memberPicker = $state('');

	// Snapshot initial state for isDirty detection
	let initialType: 'urltest' | 'selector' | 'direct' = $state('urltest');
	let initialTag = $state('');
	let initialMembers = $state<string[]>([]);
	let initialUrl = $state('https://www.gstatic.com/generate_204');
	let initialInterval = $state('3m');
	let initialTolerance = $state(50);
	let initialDefaultOutbound = $state('');
	let initialBind = $state('');

	// Initialize snapshot when modal opens
	$effect(() => {
		if (outbound) {
			initialType = outbound.type === 'selector' ? 'selector' : outbound.type === 'direct' ? 'direct' : 'urltest';
			initialTag = outbound.tag;
			initialMembers = [...(outbound.outbounds ?? [])];
			initialUrl = outbound.url ?? 'https://www.gstatic.com/generate_204';
			initialInterval = outbound.interval ?? '3m';
			initialTolerance = outbound.tolerance ?? 50;
			initialDefaultOutbound = outbound.default ?? '';
			initialBind = outbound.bind_interface ?? '';
		} else {
			initialType = 'urltest';
			initialTag = '';
			initialMembers = [];
			initialUrl = 'https://www.gstatic.com/generate_204';
			initialInterval = '3m';
			initialTolerance = 50;
			initialDefaultOutbound = '';
			initialBind = '';
		}
	});

	const isDirty = $derived.by(() => {
		return (
			type !== initialType ||
			tag !== initialTag ||
			[...members].join(',') !== [...initialMembers].join(',') ||
			url !== initialUrl ||
			interval !== initialInterval ||
			tolerance !== initialTolerance ||
			defaultOutbound !== initialDefaultOutbound ||
			bindInterface !== initialBind
		);
	});

	// Flat options with group labels for the Dropdown native grouping.
	// Filter out tags already added so the user can't pick duplicates.
	const memberDropdownOptions = $derived<DropdownOption[]>(
		outboundOptions.flatMap((g) =>
			g.items
				.filter((i) => !members.includes(i.value))
				.map((i) => ({ value: i.value, label: i.label, group: g.group }))
		)
	);

	// Default-picker options: only members already chosen. Подписочные
	// тэги (sub-XXX-YYY) и awg-XXX тэги резолвим в человеческие labels —
	// тот же UX, что и на карточке composite outbound (issue #214).
	const subsData = $derived($subscriptionsStore?.data ?? []);
	function memberLabel(tag: string): string {
		return resolveMemberLabel(tag, subsData, outboundOptions);
	}
	const defaultOptions = $derived<DropdownOption[]>(
		members.map((m) => ({ value: m, label: memberLabel(m) }))
	);

	function addMember(v: string): void {
		if (!v) return;
		if (members.includes(v)) return;
		members = [...members, v];
		// Reset picker so the same slot can be reused for the next addition.
		memberPicker = '';
	}

	function removeMember(v: string): void {
		members = members.filter((m) => m !== v);
		if (defaultOutbound === v) defaultOutbound = '';
	}

	async function save(): Promise<void> {
		busy = true;
		error = '';
		try {
			if (!tag.trim()) {
				error = 'Tag обязателен';
				busy = false;
				return;
			}

			let built: SingboxRouterOutbound;
			if (type === 'direct') {
				if (!bindInterface) {
					error = 'Выберите интерфейс';
					busy = false;
					return;
				}
				built = { type: 'direct', tag: tag.trim(), bind_interface: bindInterface };
			} else {
				if (members.length < 2) {
					error = 'Нужно минимум 2 члена';
					busy = false;
					return;
				}
				built = {
					type,
					tag: tag.trim(),
					outbounds: [...members],
				};
				if (type === 'urltest') {
					built.url = url;
					built.interval = interval;
					built.tolerance = tolerance;
				} else {
					built.default = defaultOutbound || members[0];
				}
			}

			await onSave(built);
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}

	const typeDescription = $derived(
		type === 'direct'
			? 'Прямое соединение через выбранный интерфейс (IPSec/IKEv2/др. VPN). Трафик правила пойдёт через этот интерфейс.'
			: type === 'urltest'
				? 'Периодически пингует каждого члена и автоматически направляет через самого быстрого.'
				: 'Ручное переключение через Clash API. Новые подключения идут через выбранный default.'
	);
</script>

<Modal open onclose={onClose} title={outbound ? 'Редактировать outbound' : 'Новый outbound'} hasUnsavedChanges={() => isDirty}>
	<div class="form">
		<div class="section-label">Тип</div>
		<div class="segment">
			<button class:active={type === 'urltest'} onclick={() => (type = 'urltest')} type="button" disabled={!!outbound && outbound.type === 'direct'}>URLTest</button>
			<button class:active={type === 'selector'} onclick={() => (type = 'selector')} type="button" disabled={!!outbound && outbound.type === 'direct'}>Selector</button>
			<button class:active={type === 'direct'} onclick={() => (type = 'direct')} type="button" disabled={!!outbound && outbound.type !== 'direct'}>Интерфейс</button>
		</div>
		<div class="type-hint">{typeDescription}</div>

		<label class="field">
			<div class="lbl">Tag (имя)</div>
			<input bind:value={tag} placeholder="fast-de" />
		</label>

		{#if type !== 'direct'}
			<div class="field">
				<div class="lbl">Members (минимум 2)</div>
				<div class="member-chips" class:empty={members.length === 0}>
					{#if members.length === 0}
						<span class="chips-placeholder">Участники не выбраны</span>
					{:else}
						{#each members as m (m)}
							<span class="member-chip" title={m}>
								<span class="member-chip-label">{memberLabel(m)}</span>
								<button
									type="button"
									class="member-chip-remove"
									aria-label={`Удалить ${m}`}
									title="Удалить"
									onclick={() => removeMember(m)}
								>
									<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
										<line x1="18" y1="6" x2="6" y2="18" />
										<line x1="6" y1="6" x2="18" y2="18" />
									</svg>
								</button>
							</span>
						{/each}
					{/if}
				</div>
				<Dropdown
					value={memberPicker}
					options={memberDropdownOptions}
					placeholder="Добавить участника"
					onchange={addMember}
					fullWidth
				/>
			</div>

			{#if type === 'urltest'}
				<label class="field">
					<div class="lbl">Test URL</div>
					<input bind:value={url} />
				</label>
				<div class="row2">
					<label class="field">
						<div class="lbl">Interval</div>
						<input bind:value={interval} placeholder="3m" />
					</label>
					<label class="field">
						<div class="lbl">Tolerance (ms)</div>
						<input type="number" bind:value={tolerance} />
					</label>
				</div>
			{:else}
				<div class="field">
					<div class="lbl">Default (один из members)</div>
					<Dropdown
						bind:value={defaultOutbound}
						options={defaultOptions}
						placeholder={members.length === 0 ? 'Сначала добавьте участников' : '— выбрать —'}
						disabled={members.length === 0}
						fullWidth
					/>
				</div>
			{/if}
		{/if}

		{#if type === 'direct'}
			<div class="field">
				<div class="lbl">Интерфейс</div>
				<Dropdown
					bind:value={bindInterface}
					options={bindableOptions}
					placeholder={bindablesLoading ? 'Загрузка интерфейсов…' : bindables.length === 0 ? 'Нет доступных интерфейсов' : '— выбрать интерфейс —'}
					disabled={bindablesLoading || bindables.length === 0}
					fullWidth
				/>
			</div>
		{/if}

		{#if error}<div class="error">{error}</div>{/if}
	</div>

	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={onClose} type="button">Отмена</Button>
		<Button variant="primary" size="md" onclick={save} disabled={busy} loading={busy} type="button">
			Сохранить
		</Button>
	{/snippet}
</Modal>

<style>
	.form {
		display: grid;
		gap: 0.6rem;
		min-width: 0;
	}
	.section-label {
		font-size: 0.7rem;
		text-transform: uppercase;
		letter-spacing: 0.5px;
		color: var(--muted-text);
	}
	.type-hint {
		font-size: 0.75rem;
		color: var(--muted-text);
		background: var(--bg);
		padding: 0.5rem 0.75rem;
		border-radius: 4px;
		line-height: 1.5;
	}
	.field {
		display: grid;
		gap: 0.25rem;
	}
	.lbl {
		font-size: 0.75rem;
		color: var(--muted-text);
	}
	.field input {
		background: var(--bg);
		border: 1px solid var(--border);
		padding: 0.4rem 0.6rem;
		border-radius: 4px;
		color: var(--text);
		font-family: ui-monospace, monospace;
		font-size: 0.85rem;
		width: 100%;
		box-sizing: border-box;
	}
	.segment {
		display: flex;
		width: 100%;
		border: 1px solid var(--border);
		border-radius: 4px;
		overflow: hidden;
	}
	.segment button {
		flex: 1 1 0;
		min-width: 0;
		background: transparent;
		border: none;
		padding: 0.4rem 0.9rem;
		font-size: 0.85rem;
		cursor: pointer;
		color: var(--muted-text);
		text-align: center;
		white-space: nowrap;
	}
	.segment button + button {
		border-left: 1px solid var(--border);
	}
	.segment button.active {
		background: var(--accent, #3b82f6);
		color: var(--color-accent-contrast, #ffffff);
		font-weight: 600;
	}
	.row2 {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 0.5rem;
	}
	.error {
		color: var(--danger, #dc2626);
		font-size: 0.85rem;
	}

	.member-chips {
		display: flex;
		flex-wrap: wrap;
		gap: 0.35rem;
		padding: 0.4rem 0.5rem;
		min-height: 2.1rem;
		background: var(--bg);
		border: 1px solid var(--border);
		border-radius: 4px;
		align-items: center;
	}
	.member-chips.empty {
		justify-content: flex-start;
	}
	.chips-placeholder {
		font-size: 0.78rem;
		color: var(--muted-text);
		font-style: italic;
	}
	.member-chip {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		background: var(--color-accent-tint, var(--bg));
		color: var(--color-accent, var(--text));
		border: 1px solid var(--color-accent-border, var(--border));
		border-radius: 999px;
		padding: 0.15rem 0.25rem 0.15rem 0.6rem;
		font-family: ui-monospace, monospace;
		font-size: 0.78rem;
		line-height: 1.3;
		max-width: 100%;
	}
	.member-chip-label {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.member-chip-remove {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 1.1rem;
		height: 1.1rem;
		padding: 0;
		background: transparent;
		border: none;
		border-radius: 999px;
		color: inherit;
		opacity: 0.7;
		cursor: pointer;
		transition: opacity var(--t-fast, 120ms) ease, background var(--t-fast, 120ms) ease;
	}
	.member-chip-remove:hover {
		opacity: 1;
		background: rgba(0, 0, 0, 0.12);
	}
	.member-chip-remove svg {
		width: 12px;
		height: 12px;
	}
</style>
