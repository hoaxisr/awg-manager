<script lang="ts">
	// Модалка добавления абонента (Дополнение №3 микрокопии): форма SH-39..43
	// ушла из блока «Абоненты» в окно по кнопке шапки. Поля свои, решение об
	// отправке — тоже; что делать с введённым, знает владелец списка.
	import { Button, Input, Modal } from '$lib/components/ui';

	interface Props {
		open: boolean;
		/** Общий замок мутаций сервера занят — отправлять нечего. */
		busy?: boolean;
		/** Отказ последней попытки. Печатается здесь: за закрытой модалкой
		 *  тост не связать с полями, из-за которых он пришёл. */
		error?: string;
		onsubmit: (values: { comment: string; password: string; vkHash: string }) => void;
		onclose: () => void;
	}

	let { open, busy = false, error = '', onsubmit, onclose }: Props = $props();

	let name = $state('');
	let password = $state('');
	let vkHash = $state('');

	const canSubmit = $derived(!!name.trim() && !busy);

	// Закрытая модалка полей не хранит: следующее открытие начинается с чистой
	// формы независимо от того, чем кончилось прошлое.
	$effect(() => {
		if (open) return;
		name = '';
		password = '';
		vkHash = '';
	});

	function submit() {
		if (!canSubmit) return;
		onsubmit({ comment: name.trim(), password: password.trim(), vkHash: vkHash.trim() });
	}
</script>

<!-- Клик по подложке форму не теряет: выход — «Отменить» или Esc. -->
<Modal {open} title="Новый абонент" size="sm" closeOnBackdrop={false} {onclose}>
	<div class="add-form">
		<Input label="Имя абонента" placeholder="Ноутбук Пети" bind:value={name} fullWidth />
		<Input label="Пароль" placeholder="Пусто — сгенерируется" bind:value={password} fullWidth />
		<Input label="VK-хеш" hint="По желанию" bind:value={vkHash} fullWidth />
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

	.add-error {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-error);
	}
</style>
