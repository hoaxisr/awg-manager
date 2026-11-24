<script lang="ts">
	// Добавление пользователя Telegram. Запрос create_user — здесь, у формы:
	// владелец карточки получает новый статус через oncreated и перечитывает данные.
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type { TgWebProxyStatus } from '$lib/types';
	import { Button, Modal } from '$lib/components/ui';
	import { isValidSecretInput } from './tgProxyModel';

	interface Props {
		open: boolean;
		/** Сколько пользователей уже есть — для имени по умолчанию. */
		usersCount: number;
		oncreated: (status: TgWebProxyStatus) => void | Promise<void>;
	}

	let { open = $bindable(false), usersCount, oncreated }: Props = $props();

	let newUserRemark = $state('');
	let newUserSecret = $state('');
	let addUserLoading = $state(false);

	async function handleAddUser() {
		const trimmedSec = newUserSecret.trim();
		if (trimmedSec && !isValidSecretInput(trimmedSec)) {
			notifications.error(m.tg_user_secret_error());
			return;
		}
		addUserLoading = true;
		try {
			const status = await api.tgWebProxyAction('create_user', {
				// Имя по умолчанию — данные на роутере, не зависит от языка UI.
				remark: newUserRemark.trim() || `Пользователь ${usersCount + 1}`,
				...(trimmedSec ? { secret: trimmedSec } : {}),
			});
			newUserRemark = '';
			newUserSecret = '';
			open = false;
			notifications.success(m.servers_tg_user_added());
			await oncreated(status);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_tg_user_add_error());
		} finally {
			addUserLoading = false;
		}
	}
</script>

<Modal bind:open title={m.servers_tg_add_user_title()} onclose={() => (open = false)} size="sm">
	<div class="egress-modal-body">
		<p class="field-hint">
			{m.servers_tg_add_user_hint()}
		</p>
		<label class="form-field">
			<span class="field-label">{m.servers_tg_add_user_name_label()}</span>
			<input
				type="text"
				bind:value={newUserRemark}
				class="text-input"
				placeholder={m.servers_tg_add_user_name_placeholder()}
				onkeydown={(e) => {
					if (e.key === 'Enter' && !addUserLoading) void handleAddUser();
				}}
			/>
		</label>
		<label class="form-field">
			<span class="field-label">{m.tg_user_secret_optional_label()}</span>
			<input
				type="text"
				bind:value={newUserSecret}
				class="text-input font-mono"
				placeholder={m.tg_user_secret_optional_placeholder()}
				onkeydown={(e) => {
					if (e.key === 'Enter' && !addUserLoading) void handleAddUser();
				}}
			/>
		</label>
	</div>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => (open = false)} disabled={addUserLoading}>
			{m.common_cancel()}
		</Button>
		<Button variant="primary" onclick={handleAddUser} disabled={addUserLoading}>
			{addUserLoading ? m.servers_tg_creating() : m.common_create()}
		</Button>
	{/snippet}
</Modal>

<style>
	.egress-modal-body {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.form-field {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.field-label {
		font-size: 12px;
		font-weight: 500;
		color: var(--color-text-primary);
	}

	.field-hint {
		font-size: 11.5px;
		color: var(--color-text-muted);
		margin: 0;
	}

	.text-input {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 6px 10px;
		font-size: 13px;
		color: var(--color-text-primary);
	}

	.font-mono {
		font-family: var(--font-mono);
	}
</style>
