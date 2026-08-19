<script lang="ts">
	import { Button, Modal } from '$lib/components/ui';
	import { AlertTriangle } from 'lucide-svelte';
	import PortAddressPills from './PortAddressPills.svelte';
	import type { GroupedProcessPort } from './types';

	interface Props {
		group: GroupedProcessPort | null;
		signal: 'SIGTERM' | 'SIGKILL';
		busy: boolean;
		onclose: () => void;
		onconfirm: () => void;
	}

	let { group, signal = $bindable(), busy, onclose, onconfirm }: Props = $props();
</script>

<Modal
	open={!!group}
	title={`Освободить порт ${group?.port ?? ''}?`}
	size="md"
	{onclose}
>
	{#if group}
		<div class="modal-body">
			<p>
				Вы действительно хотите завершить процесс <strong>{group.processName || 'без имени'}</strong> (PID: <code>{group.pid}</code>), занимающий порт <strong>{group.port}</strong>?
			</p>

			<div class="modal-addrs">
				<strong>Будут освобождены адреса:</strong>
				<PortAddressPills addresses={group.addresses} marginTop="0.3rem" />
			</div>

			{#if group.isSelf}
				<div class="modal-alert danger">
					<AlertTriangle size={18} />
					<div>
						<strong>Внимание!</strong> Это процесс текущего сервера <code>awg-manager</code>. Завершение немедленно прервёт работу веб-интерфейса.
					</div>
				</div>
			{:else if group.isCritical}
				<div class="modal-alert warning">
					<AlertTriangle size={18} />
					<div>
						<strong>Внимание!</strong> Этот процесс (<code>{group.processName}</code>) является системным (SSH/NDM). Завершение может нарушить доступ к роутеру.
					</div>
				</div>
			{/if}

			{#if group.cmdline}
				<div class="cmd-box">
					<div class="cmd-label">Команда запуска:</div>
					<code>{group.cmdline}</code>
				</div>
			{/if}

			<div class="signal-selector">
				<span class="sig-label">Тип сигнала:</span>
				<label class="sig-option">
					<input type="radio" name="killSignal" value="SIGTERM" bind:group={signal} />
					<span><strong>SIGTERM</strong> (Мягкое завершение процесса, рекомендуется)</span>
				</label>
				<label class="sig-option">
					<input type="radio" name="killSignal" value="SIGKILL" bind:group={signal} />
					<span><strong>SIGKILL</strong> (Принудительное немедленное убийство процесса)</span>
				</label>
			</div>
		</div>
	{/if}

	{#snippet actions()}
		<div class="modal-footer-btns">
			<Button variant="ghost" onclick={onclose}>Отмена</Button>
			<Button variant="danger" loading={busy} onclick={onconfirm}>
				Завершить ({signal})
			</Button>
		</div>
	{/snippet}
</Modal>

<style>
	.modal-body {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
		font-size: 0.9rem;
		color: var(--color-text-primary);
	}
	.modal-alert {
		display: flex;
		gap: 0.6rem;
		padding: 0.6rem 0.8rem;
		border-radius: 6px;
		font-size: 0.82rem;
		align-items: flex-start;
	}
	.modal-alert.danger {
		background: var(--color-error-tint, rgba(239, 68, 68, 0.15));
		border: 1px solid var(--color-error-border, rgba(239, 68, 68, 0.3));
		color: var(--color-error, #fca5a5);
	}
	.modal-alert.warning {
		background: var(--color-warning-tint, rgba(234, 179, 8, 0.15));
		border: 1px solid var(--color-warning-border, rgba(234, 179, 8, 0.3));
		color: var(--color-warning, #fde047);
	}

	.modal-addrs {
		font-size: 0.85rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		padding: 0.5rem 0.65rem;
		border-radius: 6px;
	}

	.cmd-box {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		padding: 0.5rem 0.65rem;
		border-radius: 6px;
		font-size: 0.78rem;
		max-height: 90px;
		overflow-y: auto;
	}
	.cmd-label {
		font-weight: 600;
		color: var(--color-text-secondary);
		margin-bottom: 0.2rem;
	}

	.signal-selector {
		display: flex;
		flex-direction: column;
		gap: 0.4rem;
		padding-top: 0.4rem;
		border-top: 1px solid var(--color-border);
	}
	.sig-label {
		font-weight: 600;
		font-size: 0.82rem;
		color: var(--color-text-secondary);
	}
	.sig-option {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		cursor: pointer;
		font-size: 0.84rem;
		color: var(--color-text-primary);
	}
</style>
