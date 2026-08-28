<script lang="ts">
	import { Button } from '$lib/components/ui';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { downloadBlob } from '$lib/utils/download';
	import { notifications } from '$lib/stores/notifications';

	interface Props {
		wgConf: string;
		title?: string;
		hint?: string;
		filename?: string;
		onImportTunnel?: () => void | Promise<void>;
		importingTunnel?: boolean;
		importDisabled?: boolean;
	}

	let {
		wgConf,
		title = 'WireGuard из ссылки',
		hint = 'Конфиг с Endpoint под listen клиента. Можно скопировать, скачать .conf или создать AWG-туннель.',
		filename = 'wdtt-client.conf',
		onImportTunnel,
		importingTunnel = false,
		importDisabled = false
	}: Props = $props();

	const trimmed = $derived(wgConf.trim());

	// TS-21 / TS-22 — тексты тостов панели (утверждены как есть, «Дополнение №1»).
	async function copyConf() {
		if (!trimmed) return;
		if (await copyToClipboard(trimmed)) {
			notifications.success('WG-конфиг скопирован');
		} else {
			notifications.error('Не удалось скопировать');
		}
	}

	function downloadConf() {
		if (!trimmed) return;
		downloadBlob(new Blob([trimmed + '\n'], { type: 'text/plain' }), filename);
		notifications.success('Файл скачан');
	}
</script>

{#if trimmed}
	<section class="wg-export">
		<div class="wg-export-head">
			<span class="section-label">{title}</span>
			{#if hint}
				<p class="wg-export-hint">{hint}</p>
			{/if}
		</div>
		<textarea class="wg-export-text" readonly rows="8" value={trimmed}></textarea>
		<div class="wg-export-actions">
			<Button variant="secondary" size="sm" onclick={copyConf}>Копировать</Button>
			<Button variant="secondary" size="sm" onclick={downloadConf}>Скачать .conf</Button>
			{#if onImportTunnel}
				<Button
					variant="primary"
					size="sm"
					loading={importingTunnel}
					disabled={importDisabled || importingTunnel}
					onclick={() => onImportTunnel?.()}
				>
					Создать AWG-туннель
				</Button>
			{/if}
		</div>
	</section>
{/if}

<style>
	.wg-export {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		padding: 0.75rem;
		border: 1px solid var(--color-accent-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
	}
	.wg-export-head {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}
	.wg-export-hint {
		margin: 0;
		font-size: 0.75rem;
		color: var(--color-text-secondary);
	}
	.wg-export-text {
		width: 100%;
		font-family: var(--font-mono);
		font-size: 0.6875rem;
		line-height: 1.4;
		padding: 0.5rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		resize: vertical;
	}
	.wg-export-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}
</style>
