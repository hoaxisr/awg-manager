<script lang="ts">
	import Modal from '$lib/components/ui/Modal.svelte';
	import type { SingboxRouterOutbound } from '$lib/types';
	import type { OutboundGroup } from './outboundOptions';

	interface Props {
		outbound?: SingboxRouterOutbound;
		outboundOptions: OutboundGroup[];
		onClose: () => void;
		onSave: (o: SingboxRouterOutbound) => Promise<void> | void;
	}
	let { outbound, outboundOptions, onClose, onSave }: Props = $props();

	// svelte-ignore state_referenced_locally
	let type: 'urltest' | 'selector' | 'loadbalance' = $state(
		(outbound?.type as 'urltest' | 'selector' | 'loadbalance') ?? 'urltest'
	);
	// svelte-ignore state_referenced_locally
	let tag = $state(outbound?.tag ?? '');
	// svelte-ignore state_referenced_locally
	let membersStr = $state((outbound?.outbounds ?? []).join(', '));
	// svelte-ignore state_referenced_locally
	let url = $state(outbound?.url ?? 'https://www.gstatic.com/generate_204');
	// svelte-ignore state_referenced_locally
	let interval = $state(outbound?.interval ?? '3m');
	// svelte-ignore state_referenced_locally
	let tolerance = $state(outbound?.tolerance ?? 50);
	// svelte-ignore state_referenced_locally
	let defaultOutbound = $state(outbound?.default ?? '');
	// svelte-ignore state_referenced_locally
	let strategy = $state(outbound?.strategy ?? 'round_robin');

	let busy = $state(false);
	let error = $state('');

	const allMemberOptions = $derived(outboundOptions.flatMap((g) => g.items.map((i) => i.value)));

	async function save(): Promise<void> {
		busy = true;
		error = '';
		try {
			if (!tag.trim()) {
				error = 'Tag обязателен';
				busy = false;
				return;
			}
			const members = membersStr.split(',').map((s) => s.trim()).filter(Boolean);
			if (members.length < 2) {
				error = 'Нужно минимум 2 члена';
				busy = false;
				return;
			}

			const built: SingboxRouterOutbound = {
				type,
				tag: tag.trim(),
				outbounds: members,
			};
			if (type === 'urltest') {
				built.url = url;
				built.interval = interval;
				built.tolerance = tolerance;
			} else if (type === 'selector') {
				built.default = defaultOutbound || members[0];
			} else if (type === 'loadbalance') {
				built.strategy = strategy;
			}

			await onSave(built);
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}

	const typeDescription = $derived.by(() => {
		if (type === 'urltest') return 'Периодически пингует каждого члена и автоматически направляет через самого быстрого.';
		if (type === 'selector') return 'Ручное переключение через Clash API. Новые подключения идут через выбранный default.';
		return 'Равномерно распределяет нагрузку между членами по выбранной стратегии.';
	});
</script>

<Modal open onclose={onClose} title={outbound ? 'Редактировать outbound' : 'Новый outbound'}>
	<div class="form">
		<div class="section-label">Тип</div>
		<div class="segment">
			<button class:active={type === 'urltest'} onclick={() => (type = 'urltest')} type="button">URLTest</button>
			<button class:active={type === 'selector'} onclick={() => (type = 'selector')} type="button">Selector</button>
			<button class:active={type === 'loadbalance'} onclick={() => (type = 'loadbalance')} type="button">LoadBalance</button>
		</div>
		<div class="type-hint">{typeDescription}</div>

		<label class="field">
			<div class="lbl">Tag (имя)</div>
			<input bind:value={tag} placeholder="fast-de" />
		</label>

		<label class="field">
			<div class="lbl">Members (через запятую)</div>
			<input bind:value={membersStr} placeholder="awg10, awg11, Germany VLESS" list="member-options" />
			<datalist id="member-options">
				{#each allMemberOptions as opt}
					<option value={opt}></option>
				{/each}
			</datalist>
		</label>

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
		{:else if type === 'selector'}
			<label class="field">
				<div class="lbl">Default</div>
				<input bind:value={defaultOutbound} placeholder="tag одного из members" />
			</label>
		{:else}
			<label class="field">
				<div class="lbl">Strategy</div>
				<select bind:value={strategy}>
					<option value="round_robin">round_robin</option>
					<option value="consistent_hashing">consistent_hashing</option>
				</select>
			</label>
		{/if}

		{#if error}<div class="error">{error}</div>{/if}

		<div class="actions">
			<button class="btn btn-secondary" onclick={onClose} type="button">Отмена</button>
			<button class="btn btn-primary" onclick={save} disabled={busy} type="button">Сохранить</button>
		</div>
	</div>
</Modal>

<style>
	.form {
		display: grid;
		gap: 0.6rem;
		min-width: 420px;
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
	.field input,
	.field select {
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
		display: inline-flex;
		border: 1px solid var(--border);
		border-radius: 4px;
		overflow: hidden;
	}
	.segment button {
		background: transparent;
		border: none;
		padding: 0.4rem 0.9rem;
		font-size: 0.85rem;
		cursor: pointer;
		color: var(--muted-text);
	}
	.segment button + button {
		border-left: 1px solid var(--border);
	}
	.segment button.active {
		background: var(--accent, #3b82f6);
		color: white;
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
	.actions {
		display: flex;
		justify-content: flex-end;
		gap: 0.5rem;
	}
</style>
