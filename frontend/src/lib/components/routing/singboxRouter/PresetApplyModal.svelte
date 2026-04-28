<script lang="ts">
	import Modal from '$lib/components/ui/Modal.svelte';
	import type { SingboxRouterPreset } from '$lib/types';
	import type { OutboundGroup } from './outboundOptions';

	interface Props {
		preset: SingboxRouterPreset;
		outboundOptions: OutboundGroup[];
		onClose: () => void;
		onApply: (id: string, outbound: string) => Promise<void> | void;
	}
	let { preset, outboundOptions, onClose, onApply }: Props = $props();

	let selectedOutbound = $state('');
	let busy = $state(false);
	let error = $state('');

	const needsOutbound = $derived(preset.rules.some((r) => r.actionTarget === 'tunnel'));

	const specialHint = $derived.by(() => {
		if (preset.rules.every((r) => r.actionTarget === 'reject')) return 'Весь совпадающий трафик будет заблокирован (action: reject). Выбор outbound не требуется.';
		if (preset.rules.every((r) => r.actionTarget === 'direct')) return 'Совпадающий трафик пойдёт мимо VPN (direct). Выбор outbound не требуется.';
		return '';
	});

	async function apply(): Promise<void> {
		busy = true;
		error = '';
		try {
			await onApply(preset.id, selectedOutbound);
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}
</script>

<Modal open onclose={onClose} title={`Применить пресет: ${preset.name}`}>
	<div class="form">
		<div class="preview">
			<div class="preview-head">Будет добавлено:</div>
			<ul>
				{#each preset.ruleSets as rs}
					<li>Rule set <code>{rs.tag}</code> (если ещё не добавлен)</li>
				{/each}
				{#each preset.rules as r}
					<li>Правило <code>rule_set: {r.ruleSetRef} → {r.actionTarget === 'tunnel' ? '«выбранный outbound»' : r.actionTarget}</code></li>
				{/each}
			</ul>
		</div>

		{#if preset.notice}
			<div class="notice">{preset.notice}</div>
		{/if}

		{#if needsOutbound}
			<label class="field">
				<div class="lbl">Направить трафик в</div>
				<select bind:value={selectedOutbound}>
					<option value="">— выберите —</option>
					{#each outboundOptions as group}
						<optgroup label={group.group}>
							{#each group.items as item}
								<option value={item.value}>{item.label}</option>
							{/each}
						</optgroup>
					{/each}
				</select>
			</label>
		{:else if specialHint}
			<div class="special-hint">{specialHint}</div>
		{/if}

		{#if error}<div class="error">{error}</div>{/if}

		<div class="actions">
			<button class="btn btn-secondary" onclick={onClose} type="button">Отмена</button>
			<button class="btn btn-primary" onclick={apply} disabled={busy || (needsOutbound && !selectedOutbound)} type="button">
				Применить
			</button>
		</div>
	</div>
</Modal>

<style>
	.form {
		display: grid;
		gap: 0.75rem;
		min-width: 380px;
	}
	.preview {
		padding: 0.75rem;
		background: var(--bg);
		border-radius: 4px;
		font-size: 0.85rem;
	}
	.preview-head {
		color: var(--muted-text);
		margin-bottom: 0.35rem;
	}
	.preview ul {
		margin: 0;
		padding-left: 1.25rem;
		color: var(--text);
	}
	.preview li {
		margin: 0.15rem 0;
	}
	.preview code {
		font-family: ui-monospace, monospace;
		font-size: 0.8rem;
		color: var(--accent, #3b82f6);
	}
	.notice {
		padding: 0.5rem 0.75rem;
		background: rgba(224, 175, 104, 0.12);
		border-left: 3px solid var(--warning);
		border-radius: 4px;
		font-size: 0.8rem;
		line-height: 1.4;
		color: var(--text);
	}
	.special-hint {
		padding: 0.5rem 0.75rem;
		background: var(--bg);
		border-left: 2px solid var(--accent, #3b82f6);
		border-radius: 4px;
		font-size: 0.8rem;
		color: var(--muted-text);
	}
	.field {
		display: grid;
		gap: 0.25rem;
	}
	.lbl {
		font-size: 0.75rem;
		color: var(--muted-text);
	}
	.field select {
		background: var(--bg);
		border: 1px solid var(--border);
		padding: 0.45rem 0.6rem;
		border-radius: 4px;
		color: var(--text);
		font-family: ui-monospace, monospace;
		font-size: 0.85rem;
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
