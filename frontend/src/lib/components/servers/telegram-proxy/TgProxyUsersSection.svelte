<script lang="ts">
	// Список пользователей прокси Telegram: включение, имя, ссылка/QR, удаление.
	import { m } from '$lib/i18n';
	import type { TelegramUser } from '$lib/types';
	import { Button, IconButton, Toggle } from '$lib/components/ui';
	import { Check, Pencil, Plus, QrCode, Trash2, X } from 'lucide-svelte';

	interface Props {
		users: TelegramUser[];
		/** id пользователя, над которым идёт действие. */
		busyId: string;
		onadd: () => void;
		ontoggle: (user: TelegramUser) => void;
		/** Новое имя — уже обрезанное, непустое и отличное от прежнего. */
		onrename: (user: TelegramUser, remark: string) => Promise<boolean | void> | boolean | void;
		onreveal: (user: TelegramUser) => void;
		ondelete: (user: TelegramUser) => void;
	}

	let { users, busyId, onadd, ontoggle, onrename, onreveal, ondelete }: Props = $props();

	let editingUserId = $state<string>('');
	let editingRemark = $state<string>('');
	let renameSubmitting = $state(false);
	let anyBusy = $derived(Boolean(busyId) || renameSubmitting);

	$effect(() => {
		if (editingUserId && !users.some((u) => u.id === editingUserId)) {
			editingUserId = '';
			editingRemark = '';
		}
	});

	function startRename(user: TelegramUser) {
		if (anyBusy) return;
		editingUserId = user.id;
		editingRemark = user.remark;
	}

	function cancelRename() {
		if (renameSubmitting) return;
		editingUserId = '';
		editingRemark = '';
	}

	async function submitRename(user: TelegramUser) {
		if (anyBusy) return;
		const cleaned = editingRemark.trim();
		if (!cleaned) return;
		if (cleaned === user.remark) {
			cancelRename();
			return;
		}
		renameSubmitting = true;
		try {
			const res = await onrename(user, cleaned);
			if (res !== false && editingUserId === user.id) {
				editingUserId = '';
				editingRemark = '';
			}
		} finally {
			renameSubmitting = false;
		}
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
							disabled={anyBusy}
							onchange={() => ontoggle(user)}
						/>
						{#if editingUserId === user.id}
							<div class="tg-user-rename-inline">
								<input
									type="text"
									maxlength={64}
									class="tg-user-rename-input"
									bind:value={editingRemark}
									aria-label={m.servers_tg_user_rename()}
									placeholder={m.servers_tg_user_rename()}
									disabled={anyBusy}
									onkeydown={(e) => {
										if (e.key === 'Enter') {
											e.preventDefault();
											void submitRename(user);
										} else if (e.key === 'Escape') {
											e.preventDefault();
											cancelRename();
										}
									}}
								/>
								<IconButton
									size="sm"
									title={m.common_save()}
									ariaLabel={m.common_save()}
									disabled={anyBusy || !editingRemark.trim()}
									onclick={() => void submitRename(user)}
								>
									<Check size={14} />
								</IconButton>
								<IconButton
									size="sm"
									title={m.common_cancel()}
									ariaLabel={m.common_cancel()}
									disabled={anyBusy}
									onclick={cancelRename}
								>
									<X size={14} />
								</IconButton>
							</div>
						{:else}
							<span class="tg-user-name" class:muted-name={!user.enabled}>{user.remark}</span>
							<IconButton
								size="sm"
								title={m.servers_tg_user_rename()}
								ariaLabel={m.servers_tg_user_rename()}
								disabled={anyBusy}
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
							disabled={anyBusy}
							onclick={() => {
								if (editingUserId === user.id) {
									editingUserId = '';
									editingRemark = '';
								}
								ondelete(user);
							}}
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

	.tg-user-rename-inline {
		display: inline-flex;
		align-items: center;
		gap: 4px;
	}

	.tg-user-rename-input {
		font-size: 13px;
		padding: 4px 8px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-accent-border, var(--color-border));
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		min-width: 140px;
		max-width: 220px;
	}

	.tg-user-actions {
		display: flex;
		align-items: center;
		gap: 8px;
	}
</style>
