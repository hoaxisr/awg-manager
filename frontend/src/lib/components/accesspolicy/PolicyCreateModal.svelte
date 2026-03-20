<script lang="ts">
	import { Modal } from '$lib/components/ui';

	interface Props {
		open: boolean;
		saving: boolean;
		oncreate: (description: string) => void;
		onclose: () => void;
	}

	let { open, saving, oncreate, onclose }: Props = $props();

	let description = $state('');

	$effect(() => {
		if (open) {
			description = '';
		}
	});

	function handleSubmit(e: SubmitEvent) {
		e.preventDefault();
		if (description.trim()) {
			oncreate(description.trim());
		}
	}
</script>

<Modal {open} title="Создать политику" size="sm" {onclose}>
	<form onsubmit={handleSubmit}>
		<label class="field-label">
			Описание
			<input
				type="text"
				class="field-input"
				bind:value={description}
				placeholder="Например: Гостевая сеть"
				required
				disabled={saving}
			/>
		</label>
	</form>

	{#snippet actions()}
		<button class="btn btn-ghost" onclick={onclose} disabled={saving}>Отмена</button>
		<button
			class="btn btn-primary"
			onclick={() => description.trim() && oncreate(description.trim())}
			disabled={saving || !description.trim()}
		>
			{#if saving}Создание...{:else}Создать{/if}
		</button>
	{/snippet}
</Modal>

<style>
	.field-label {
		display: flex;
		flex-direction: column;
		gap: 6px;
		font-size: 0.875rem;
		font-weight: 500;
		color: var(--text-primary);
	}

	.field-input {
		width: 100%;
		padding: 8px 12px;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--bg-primary);
		color: var(--text-primary);
		font-size: 0.875rem;
		outline: none;
		transition: border-color 0.15s;
	}

	.field-input:focus {
		border-color: var(--accent);
	}

	.field-input:disabled {
		opacity: 0.6;
	}
</style>
