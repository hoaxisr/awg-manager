<script lang="ts">
	// Подтверждение удаления пользователя Telegram.
	import { m } from '$lib/i18n';
	import type { TelegramUser } from '$lib/types';
	import { Button, Modal } from '$lib/components/ui';
	import { AlertTriangle, Trash2 } from 'lucide-svelte';

	interface Props {
		target: TelegramUser | null;
		loading: boolean;
		/** Удаляется последний пользователь — все клиенты потеряют доступ. */
		isLast: boolean;
		onconfirm: () => void;
		oncancel: () => void;
	}

	let { target, loading, isLast, onconfirm, oncancel }: Props = $props();
</script>

<Modal
	open={Boolean(target)}
	title={m.tg_user_delete_confirm_title()}
	onclose={() => { if (!loading) oncancel(); }}
	size="sm"
>
	{#if target}
		<div class="confirm-content">
			<AlertTriangle size={32} class="confirm-icon text-danger mb-3" />
			<p class="confirm-p">
				{m.tg_user_delete_confirm_body({ name: target.remark })}
			</p>
			{#if isLast}
				<p class="confirm-note">
					{m.tg_user_delete_confirm_last_note()}
				</p>
			{/if}
		</div>
	{/if}

	{#snippet actions()}
		<Button variant="secondary" onclick={oncancel} disabled={loading}>
			{m.common_cancel()}
		</Button>
		<Button variant="danger" onclick={onconfirm} disabled={loading}>
			<Trash2 size={14} class="mr-1" />
			{m.tg_user_delete_confirm_action()}
		</Button>
	{/snippet}
</Modal>

<style>
	.confirm-content {
		display: flex;
		flex-direction: column;
		align-items: center;
		text-align: center;
		padding: 0.5rem 0;
	}

	.confirm-p {
		font-size: 13px;
		color: var(--color-text-primary);
		margin: 0 0 8px 0;
	}

	.confirm-note {
		font-size: 12px;
		color: var(--color-text-secondary);
		line-height: 1.4;
		margin: 0;
		background: var(--color-bg-primary);
		padding: 8px 12px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
	}
</style>
