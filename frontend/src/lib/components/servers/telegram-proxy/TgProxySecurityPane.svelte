<script lang="ts">
	// Вкладка «Ключи и безопасность»: ротация ключа, отзыв старого, сброс кэша
	// сканеров — с подтверждениями. Запросы — здесь; новый статус уходит владельцу.
	import { m, formatLocale } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type { TgWebProxyStatus } from '$lib/types';
	import { Button, Modal } from '$lib/components/ui';
	import { AlertTriangle, KeyRound, RefreshCw, ShieldCheck, Trash2 } from 'lucide-svelte';

	interface Props {
		status: TgWebProxyStatus | null;
		/** Новый статус после действия; reload — перечитать данные карточки. */
		onchanged: (status: TgWebProxyStatus, reload: boolean) => void | Promise<void>;
	}

	let { status, onchanged }: Props = $props();

	let rotateConfirmOpen = $state(false);
	let rotateSecretLoading = $state(false);
	let revokeConfirmOpen = $state(false);
	let revokeLoading = $state(false);
	let clearCacheConfirmOpen = $state(false);
	let clearCacheLoading = $state(false);

	async function executeRotateSecret() {
		rotateSecretLoading = true;
		try {
			const next = await api.tgWebProxyAction('rotate_secret');
			rotateConfirmOpen = false;
			notifications.success(m.servers_tg_rotate_success());
			await onchanged(next, true);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_tg_rotate_error());
		} finally {
			rotateSecretLoading = false;
		}
	}

	async function executeRevokeLegacy() {
		revokeLoading = true;
		try {
			const next = await api.tgWebProxyAction('revoke_legacy');
			revokeConfirmOpen = false;
			notifications.success(m.servers_tg_revoke_success());
			await onchanged(next, true);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_tg_revoke_error());
		} finally {
			revokeLoading = false;
		}
	}

	async function executeClearScannerCache() {
		clearCacheLoading = true;
		try {
			const next = await api.tgWebProxyAction('clear_scanner_cache');
			clearCacheConfirmOpen = false;
			notifications.success(m.servers_tg_clear_cache_success());
			await onchanged(next, false);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_tg_clear_cache_error());
		} finally {
			clearCacheLoading = false;
		}
	}

	function formatDate(isoStr?: string): string {
		if (!isoStr) return '';
		try {
			const d = new Date(isoStr);
			return d.toLocaleString(formatLocale(), {
				day: 'numeric',
				month: 'short',
				hour: '2-digit',
				minute: '2-digit',
			});
		} catch {
			return isoStr;
		}
	}
</script>

<div class="security-pane">
	{#if status?.legacy_active}
		<div class="grace-alert">
			<div class="grace-alert-left">
				<AlertTriangle size={18} class="grace-icon" />
				<div class="grace-text">
					<span class="grace-title">{m.servers_tg_grace_title()}</span>
					<span class="grace-desc">
						{m.servers_tg_grace_desc_before()} <strong>{formatDate(status.legacy_expires_at)}</strong>.
					</span>
				</div>
			</div>
			<Button variant="secondary" size="sm" onclick={() => (revokeConfirmOpen = true)}>
				<ShieldCheck size={14} class="mr-1" />
				{m.servers_tg_revoke_now()}
			</Button>
		</div>
	{/if}

	<div class="actions-strip">
		<div class="key-display-box">
			<KeyRound size={15} class="key-icon" />
			<span class="key-label">{m.servers_tg_label_secret_colon()}</span>
			<code class="key-code font-mono">{status?.secret_masked || '••••••••'}</code>
		</div>

		<div class="actions-right">
			<Button variant="secondary" size="sm" onclick={() => (rotateConfirmOpen = true)}>
				<RefreshCw size={14} class="mr-1" />
				{m.servers_tg_rotate_btn()}
			</Button>
			<Button variant="secondary" size="sm" onclick={() => (clearCacheConfirmOpen = true)}>
				<Trash2 size={14} class="mr-1" />
				{m.servers_tg_clear_cache_btn()}
			</Button>
		</div>
	</div>
</div>

<Modal bind:open={rotateConfirmOpen} title={m.servers_tg_rotate_title()} onclose={() => (rotateConfirmOpen = false)} size="sm">
	<div class="confirm-content">
		<AlertTriangle size={32} class="confirm-icon text-warning mb-3" />
		<p class="confirm-p">
			{m.servers_tg_rotate_body()}
		</p>
		<p class="confirm-note">
			<strong>{m.servers_tg_rotate_grace_label()}</strong> {m.servers_tg_rotate_grace_text()}
		</p>
	</div>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => (rotateConfirmOpen = false)} disabled={rotateSecretLoading}>{m.common_cancel()}</Button>
		<Button variant="primary" onclick={executeRotateSecret} disabled={rotateSecretLoading}>
			<RefreshCw size={14} class={rotateSecretLoading ? 'spin mr-1' : 'mr-1'} />
			{rotateSecretLoading ? m.servers_tg_rotating() : m.servers_tg_rotate_confirm()}
		</Button>
	{/snippet}
</Modal>

<Modal bind:open={revokeConfirmOpen} title={m.servers_tg_revoke_title()} onclose={() => (revokeConfirmOpen = false)} size="sm">
	<div class="confirm-content">
		<AlertTriangle size={32} class="confirm-icon text-danger mb-3" />
		<p class="confirm-p">
			{m.servers_tg_revoke_body_before()} <strong>{m.servers_tg_revoke_body_strong()}</strong>?
		</p>
		<p class="confirm-note">
			{m.servers_tg_revoke_note()}
		</p>
	</div>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => (revokeConfirmOpen = false)} disabled={revokeLoading}>{m.common_cancel()}</Button>
		<Button variant="danger" onclick={executeRevokeLegacy} disabled={revokeLoading}>
			<Trash2 size={14} class="mr-1" />
			{revokeLoading ? m.servers_tg_revoking() : m.servers_tg_revoke_confirm()}
		</Button>
	{/snippet}
</Modal>

<Modal bind:open={clearCacheConfirmOpen} title={m.servers_tg_clear_cache_title()} onclose={() => (clearCacheConfirmOpen = false)} size="sm">
	<div class="confirm-content">
		<ShieldCheck size={32} class="confirm-icon text-primary mb-3" />
		<p class="confirm-p">
			{m.servers_tg_clear_cache_body()}
		</p>
	</div>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => (clearCacheConfirmOpen = false)} disabled={clearCacheLoading}>{m.common_cancel()}</Button>
		<Button variant="primary" onclick={executeClearScannerCache} disabled={clearCacheLoading}>
			{clearCacheLoading ? m.servers_tg_clearing() : m.servers_tg_clear_cache_confirm()}
		</Button>
	{/snippet}
</Modal>

<style>
	.security-pane {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.grace-alert {
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		border-radius: var(--radius-sm);
		padding: 0.75rem 1rem;
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
		flex-wrap: wrap;
	}

	.grace-alert-left {
		display: flex;
		align-items: center;
		gap: 10px;
	}

	:global(.grace-icon) {
		color: var(--color-warning);
		flex-shrink: 0;
	}

	.grace-text {
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.grace-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-warning);
	}

	.grace-desc {
		font-size: 12px;
		color: var(--color-text-secondary);
	}

	.actions-strip {
		display: flex;
		align-items: center;
		justify-content: space-between;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 0.75rem 1rem;
		gap: 1rem;
		flex-wrap: wrap;
	}

	.key-display-box {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	:global(.key-icon) {
		color: var(--color-text-muted);
	}

	.key-label {
		font-size: 12px;
		color: var(--color-text-muted);
	}

	.key-code {
		font-size: 12px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		padding: 2px 8px;
		border-radius: var(--radius-sm);
		color: var(--color-text-primary);
	}

	.actions-right {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}

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

	.font-mono {
		font-family: var(--font-mono);
	}
</style>
