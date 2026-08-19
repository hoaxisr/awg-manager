<script lang="ts">
	import type { SystemProcessItem } from '$lib/api/client';
	import { Button, Modal } from '$lib/components/ui';
	import { formatBytes } from '$lib/utils/format';
	import { AlertTriangle, Square } from 'lucide-svelte';

	interface Props {
		target: SystemProcessItem | null;
		signal: 'SIGTERM' | 'SIGKILL';
		killing: boolean;
		onclose: () => void;
		onconfirm: () => void;
	}

	let { target, signal = $bindable(), killing, onclose, onconfirm }: Props = $props();
</script>

<Modal
	open={target !== null}
	title="Завершение процесса"
	size="md"
	{onclose}
>
	{#if target}
		<div class="kill-modal-content">
			<div class="kill-warning-box">
				<AlertTriangle size={24} class="warning-icon" />
				<div>
					<p>Вы действительно хотите отправить сигнал завершения процессу?</p>
					<strong>PID {target.pid} — {target.name}</strong>
				</div>
			</div>

			<div class="kill-info-table">
				<div class="kill-row">
					<span>Команда:</span>
					<code>{target.cmdline}</code>
				</div>
				<div class="kill-row">
					<span>Пользователь:</span>
					<span>{target.user}</span>
				</div>
				<div class="kill-row">
					<span>Использование:</span>
					<span>CPU: {target.cpuPercent.toFixed(1)}% | RAM: {formatBytes(target.memoryRss)}</span>
				</div>
			</div>

			{#if target.isSelf}
				<div class="self-kill-notice">
					<AlertTriangle size={16} class="danger-icon" />
					<div>
						<strong>Внимание: Это текущий процесс веб-панели AWG Manager!</strong>
						<p>При завершении процесса веб-интерфейс будет немедленно остановлен и станет недоступен. Чтобы снова его включить, потребуется зайти по SSH и выполнить: <code>/opt/etc/init.d/S99awg-manager start</code>.</p>
					</div>
				</div>
			{:else if target.isCritical}
				<div class="danger-notice">
					<AlertTriangle size={15} />
					<span>Внимание: Это критически важный системный процесс роутера! Его завершение может нарушить работу сети или доступ к устройству.</span>
				</div>
			{/if}

			<div class="signal-selector">
				<span class="signal-label">Тип сигнала:</span>
				<label class="signal-option">
					<input type="radio" bind:group={signal} value="SIGTERM" />
					<span><strong>SIGTERM</strong> (Мягкое корректное завершение)</span>
				</label>
				<label class="signal-option">
					<input type="radio" bind:group={signal} value="SIGKILL" />
					<span><strong>SIGKILL</strong> (Принудительное немедленное убийство)</span>
				</label>
			</div>
		</div>
	{/if}

	{#snippet actions()}
		<div class="modal-footer-btns">
			<Button variant="ghost" onclick={onclose}>Отмена</Button>
			<Button variant="danger" loading={killing} onclick={onconfirm}>
				{#snippet iconBefore()}<Square size={13} />{/snippet}
				Завершить (PID {target?.pid})
			</Button>
		</div>
	{/snippet}
</Modal>

<style>
	/* Kill Modal */
	.kill-modal-content {
		display: flex;
		flex-direction: column;
		gap: 0.85rem;
	}

	.kill-warning-box {
		display: flex;
		gap: 0.75rem;
		align-items: center;
		padding: 0.75rem;
		background: var(--color-bg-tertiary);
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
	}

	:global(.warning-icon) {
		color: #f59e0b;
		flex-shrink: 0;
	}

	.kill-info-table {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
		padding: 0.6rem;
		background: var(--color-bg-secondary);
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		font-size: 0.82rem;
	}

	.kill-row {
		display: flex;
		justify-content: space-between;
		gap: 0.5rem;
	}

	.kill-row code {
		word-break: break-all;
	}

	.self-kill-notice {
		display: flex;
		gap: 0.6rem;
		padding: 0.65rem;
		background: rgba(245, 158, 11, 0.12);
		border: 1px solid rgba(245, 158, 11, 0.35);
		border-radius: var(--radius-sm, 6px);
		color: #d97706;
		font-size: 0.8rem;
	}
	:global(.dark) .self-kill-notice {
		color: #fbbf24;
	}
	.self-kill-notice p {
		margin: 0.25rem 0 0 0;
	}
	.self-kill-notice code {
		background: var(--color-bg-tertiary);
		padding: 0.1rem 0.3rem;
		border-radius: 3px;
	}

	.danger-notice {
		display: flex;
		gap: 0.5rem;
		padding: 0.5rem 0.65rem;
		background: rgba(239, 68, 68, 0.12);
		border: 1px solid rgba(239, 68, 68, 0.3);
		border-radius: var(--radius-sm, 6px);
		color: #f87171;
		font-size: 0.8rem;
	}

	.signal-selector {
		display: flex;
		flex-direction: column;
		gap: 0.4rem;
		padding: 0.6rem;
		background: var(--color-bg-secondary);
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		font-size: 0.82rem;
	}

	.signal-label {
		font-weight: 600;
		color: var(--color-text-secondary);
	}

	.signal-option {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		cursor: pointer;
	}

	.modal-footer-btns {
		display: flex;
		justify-content: flex-end;
		gap: 0.5rem;
		width: 100%;
	}
</style>
