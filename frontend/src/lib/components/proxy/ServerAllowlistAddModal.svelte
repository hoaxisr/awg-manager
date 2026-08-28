<script lang="ts">
	// Модалка добавления абонента FreeTurn (Дополнение №4 п.1): та же форма, что
	// у WDTT, — окно по кнопке шапки, а не inline. Поля свои, решение об
	// отправке — у владельца списка.
	import { RefreshCw } from 'lucide-svelte';
	import { Button, IconButton, Input, Modal, Toggle } from '$lib/components/ui';

	interface Props {
		open: boolean;
		/** Общий замок мутаций сервера занят — отправлять нечего. */
		busy?: boolean;
		/** Отказ последней попытки: печатается здесь, у полей, которых он касается. */
		error?: string;
		onsubmit: (values: { clientId: string; name: string; allow: boolean }) => void;
		onclose: () => void;
	}

	let { open, busy = false, error = '', onsubmit, onclose }: Props = $props();

	/** Client ID придумывает фронт: бэкенд в ответе лишь возвращает присланный. */
	function randomClientId(): string {
		const bytes = new Uint8Array(16);
		crypto.getRandomValues(bytes);
		return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
	}

	let clientId = $state(randomClientId());
	let name = $state('');
	let allow = $state(true);

	const canSubmit = $derived(!!clientId.trim() && !busy);

	// Закрытая модалка полей не хранит: следующее открытие начинается с чистой
	// формы и нового Client ID.
	$effect(() => {
		if (open) return;
		clientId = randomClientId();
		name = '';
		allow = true;
	});

	function submit() {
		if (!canSubmit) return;
		onsubmit({ clientId: clientId.trim(), name: name.trim(), allow });
	}
</script>

<!-- Клик по подложке форму не теряет: выход — «Отменить» или Esc. -->
<Modal {open} title="Новый абонент" size="sm" closeOnBackdrop={false} {onclose}>
	<div class="add-form">
		<div class="field-with-btn">
			<Input label="Client ID" bind:value={clientId} fullWidth />
			<IconButton
				size="sm"
				ariaLabel="Обновить Client ID"
				onclick={() => (clientId = randomClientId())}
			>
				<RefreshCw size={14} />
			</IconButton>
		</div>
		<Input label="Имя абонента" bind:value={name} fullWidth />
		<Toggle
			label="Внести в список разрешённых"
			checked={allow}
			onchange={(v) => (allow = v)}
		/>
		{#if error}
			<p class="add-error" role="alert">{error}</p>
		{/if}
	</div>
	{#snippet actions()}
		<Button variant="secondary" size="md" disabled={busy} onclick={onclose}>Отменить</Button>
		<Button variant="primary" size="md" disabled={!canSubmit} loading={busy} onclick={submit}>
			Добавить
		</Button>
	{/snippet}
</Modal>

<style>
	.add-form {
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
	}

	.field-with-btn {
		display: flex;
		align-items: flex-end;
		gap: 0.375rem;
		min-width: 0;
	}

	.field-with-btn :global(svg) {
		display: block;
	}

	.add-error {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-error);
	}
</style>
