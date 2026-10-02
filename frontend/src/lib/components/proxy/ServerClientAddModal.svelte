<script lang="ts">
	// Модалка добавления абонента (Дополнение №3 микрокопии): форма SH-39..43
	// ушла из блока «Абоненты» в окно по кнопке шапки. Поля свои, решение об
	// отправке — тоже; что делать с введённым, знает владелец списка.
	import { m } from '$lib/i18n';
	import { Button, Input, Modal } from '$lib/components/ui';
	import { CLIENT_TEXT } from './serverClients';

	interface Props {
		open: boolean;
		/** VK-хеши сервера: абоненту без своего хеша ссылка подставит их
		 *  (`LinkPanel`). Пусто — подставлять нечего, и хеш обязателен. */
		serverVkHashes: string;
		/** Общий замок мутаций сервера занят — отправлять нечего. */
		busy?: boolean;
		/** Отказ последней попытки. Печатается здесь: за закрытой модалкой
		 *  тост не связать с полями, из-за которых он пришёл. */
		error?: string;
		onsubmit: (values: { comment: string; password: string; vkHash: string }) => void;
		onclose: () => void;
	}

	let { open, serverVkHashes, busy = false, error = '', onsubmit, onclose }: Props = $props();

	let name = $state('');
	let password = $state('');
	let vkHash = $state('');

	const serverHashes = $derived(serverVkHashes.trim());
	const canSubmit = $derived(!!name.trim() && !busy && (!!serverHashes || !!vkHash.trim()));

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
<Modal {open} title={m.proxy_client_add_title()} size="sm" closeOnBackdrop={false} {onclose}>
	<div class="add-form">
		<Input label={m.proxy_clients_name_label()} placeholder={m.proxy_client_add_name_placeholder()} bind:value={name} fullWidth />
		<Input label={m.proxy_clients_password_label()} placeholder={m.proxy_client_add_password_placeholder()} bind:value={password} fullWidth />
		<div class="vk-field">
			<Input label={m.proxy_clients_vk_hash_label()} bind:value={vkHash} fullWidth />
			{#if serverHashes}
				<!-- Не обещание, а факт: подставится ровно эта строка. Длинную
				     обрезаем по ширине, целиком она остаётся в титре. -->
				<p class="vk-note">
					{m.proxy_client_add_vk_empty()}
					<span class="vk-hashes" title={serverHashes}>{serverHashes}</span>
				</p>
			{:else}
				<p class="vk-note">{CLIENT_TEXT.vkHashRequired}</p>
			{/if}
		</div>
		{#if error}
			<p class="add-error" role="alert">{error}</p>
		{/if}
	</div>
	{#snippet actions()}
		<Button variant="secondary" size="md" disabled={busy} onclick={onclose}>{m.proxy_common_cancel_action()}</Button>
		<Button variant="primary" size="md" disabled={!canSubmit} loading={busy} onclick={submit}>
			{m.proxy_common_add()}
		</Button>
	{/snippet}
</Modal>

<style>
	.add-form {
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
	}

	/* Тот же зазор, что у подписи внутри поля: подпись читается как его часть. */
	.vk-field {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.vk-note {
		margin: 0;
		font-size: 12px;
		color: var(--color-text-muted);
	}

	.vk-hashes {
		display: inline-block;
		max-width: 100%;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		vertical-align: bottom;
	}

	.add-error {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-error);
	}
</style>
