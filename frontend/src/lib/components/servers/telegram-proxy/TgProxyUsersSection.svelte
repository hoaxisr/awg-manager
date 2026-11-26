<script lang="ts">
	// Список пользователей прокси Telegram: включение, имя, ссылка/QR, удаление.
	import { m } from '$lib/i18n';
	import type { TelegramUser } from '$lib/types';
	import { Button, IconButton, Input, Toggle } from '$lib/components/ui';
	import { Check, Pencil, Plus, QrCode, Trash2, X } from 'lucide-svelte';

	interface Props {
		users: TelegramUser[];
		/** id пользователя, над которым идёт действие. */
		busyId: string;
		onadd: () => void;
		ontoggle: (user: TelegramUser) => void;
		/** Новое имя — уже обрезанное, непустое и отличное от прежнего. */
		onrename: (user: TelegramUser, remark: string) => void;
		onreveal: (user: TelegramUser) => void;
		ondelete: (user: TelegramUser) => void;
	}

	let { users, busyId, onadd, ontoggle, onrename, onreveal, ondelete }: Props = $props();

	let editingId = $state('');
	let draftName = $state('');

	function startRename(user: TelegramUser) {
		editingId = user.id;
		draftName = user.remark;
	}

	function commitRename(user: TelegramUser) {
		const next = draftName.trim();
		editingId = '';
		if (next && next !== user.remark) onrename(user, next);
	}
</script>

<section class="tg-users-section">
	<div class="tg-users-header">
		<div class="tg-users-title-wrap">
			<h3 class="tg-users-title">{m.servers_tg_users_title()}</h3>
			<span class="tg-users-count">({users.length})</span>
		</div>
		<Button variant="secondary" size="sm" onclick={onadd}>
			<Plus size={14} class="mr-1" />
			{m.servers_tg_users_add()}
		</Button>
	</div>

	{#if users.length > 0}
		<div class="tg-users-list">
			{#each users as user (user.id)}
				<div class="tg-user-row">
					<div class="tg-user-left">
						<Toggle
							checked={user.enabled}
							disabled={busyId === user.id}
							onchange={() => ontoggle(user)}
						/>
						{#if editingId === user.id}
							<Input value={draftName} ariaLabel={m.servers_tg_user_rename()} oninput={(v) => (draftName = v)} />
							<IconButton
								size="sm"
								title={m.common_save()}
								ariaLabel={m.common_save()}
								disabled={!draftName.trim()}
								onclick={() => commitRename(user)}
							>
								<Check size={14} />
							</IconButton>
							<IconButton
								size="sm"
								title={m.common_cancel()}
								ariaLabel={m.common_cancel()}
								onclick={() => (editingId = '')}
							>
								<X size={14} />
							</IconButton>
						{:else}
							<span class="tg-user-name" class:muted-name={!user.enabled}>{user.remark}</span>
							<IconButton
								size="sm"
								title={m.servers_tg_user_rename()}
								ariaLabel={m.servers_tg_user_rename()}
								disabled={busyId === user.id}
								onclick={() => startRename(user)}
							>
								<Pencil size={13} />
							</IconButton>
						{/if}
						<span class="badge {user.enabled ? 'badge-success' : 'badge-neutral'}">
							{user.enabled ? m.servers_tg_user_allowed() : m.servers_tg_method_off()}
						</span>
					</div>
					<div class="tg-user-actions">
						<Button variant="primary" size="sm" onclick={() => onreveal(user)}>
							<QrCode size={13} class="mr-1" />
							{m.servers_tg_user_qr_link()}
						</Button>
						<IconButton
							variant="danger"
							size="md"
							title={m.tg_user_delete_btn()}
							ariaLabel={m.tg_user_delete_btn()}
							disabled={busyId === user.id}
							onclick={() => ondelete(user)}
						>
							<Trash2 size={14} />
						</IconButton>
					</div>
				</div>
			{/each}
		</div>
	{:else}
		<p class="field-hint">{m.tg_users_empty()}</p>
	{/if}
</section>

<style>
	.badge {
		font-size: 11px;
		padding: 2px 8px;
		border-radius: var(--radius-sm);
		font-weight: 500;
	}
	.badge-success {
		background: var(--color-success-tint);
		border: 1px solid var(--color-success-border);
		color: var(--color-success);
	}
	.badge-neutral {
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
	}

	.field-hint {
		font-size: 11.5px;
		color: var(--color-text-muted);
		margin: 0;
	}

	.tg-users-section {
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding-top: 4px;
	}

	.tg-users-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
	}

	.tg-users-title-wrap {
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.tg-users-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary);
		margin: 0;
	}

	.tg-users-count {
		font-size: 12px;
		color: var(--color-text-muted);
	}

	.tg-users-list {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.tg-user-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
		padding: 10px 14px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		transition: border-color var(--transition-fast);
	}

	.tg-user-row:hover {
		border-color: var(--color-text-muted);
	}

	.tg-user-left {
		display: flex;
		align-items: center;
		gap: 10px;
		flex-wrap: wrap;
	}

	.tg-user-name {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.tg-user-name.muted-name {
		color: var(--color-text-muted);
	}

	.tg-user-actions {
		display: flex;
		align-items: center;
		gap: 8px;
	}
</style>
