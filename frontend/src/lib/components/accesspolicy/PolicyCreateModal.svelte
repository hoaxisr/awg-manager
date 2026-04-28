<script lang="ts">
	import { Modal, Button } from '$lib/components/ui';

	interface Props {
		open: boolean;
		saving: boolean;
		oncreate: (description: string) => void;
		onclose: () => void;
	}

	let { open = $bindable(false), saving, oncreate, onclose }: Props = $props();

	let description = $state('');
	const VALID_PATTERN = /^[a-zA-Z0-9_-]*$/;
	const MAX_LEN = 256;

	let isValid = $derived(description.trim().length > 0 && description.trim().length <= MAX_LEN && VALID_PATTERN.test(description.trim()));
	let attempted = $state(false);

	let descriptionError = $derived.by(() => {
		if (!attempted) return '';
		const val = description.trim();
		if (val.length === 0) return 'Введите описание политики';
		if (!VALID_PATTERN.test(val)) return 'Только латинские буквы, цифры, дефисы и подчёркивания';
		if (val.length > MAX_LEN) return 'Максимум 256 символов';
		return '';
	});

	$effect(() => {
		if (open) {
			description = '';
			attempted = false;
		}
	});

	function handleSave() {
		attempted = true;
		if (!isValid) {
			// TODO Phase 1: restore shake animation feedback on invalid submit
			return;
		}
		oncreate(description.trim());
	}
</script>

<Modal {open} title="Создать политику" size="sm" {onclose}>
	<div class="form-group" class:field-error={descriptionError !== ''}>
		<label class="field-label">
			Описание
			<input
				type="text"
				class="field-input"
				bind:value={description}
				placeholder="Guest-Network"
				disabled={saving}
			/>
			<span class="field-hint">Латинские буквы, цифры, дефисы, подчёркивания</span>
			<div class="error-text" class:visible={descriptionError !== ''}>{descriptionError}</div>
		</label>
	</div>

	{#snippet actions()}
		<Button variant="ghost" onclick={onclose} disabled={saving}>Отмена</Button>
		<!-- TODO Phase 1: shake animation on save when invalid (was class:shake={shaking}) -->
		<Button variant="primary" onclick={handleSave} loading={saving}>
			Создать
		</Button>
	{/snippet}
</Modal>

<style>
	.form-group {
		margin-bottom: 0;
	}

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

	.field-error .field-input {
		border-color: var(--error, #ef4444);
		box-shadow: 0 0 0 2px rgba(239, 68, 68, 0.15);
	}

	.field-hint {
		font-size: 0.75rem;
		color: var(--text-secondary);
	}
</style>
