<script lang="ts">
	// Блок экспорта конфигурации клиента: подпись, «Копировать», «Скачать» и превью.
	import { m } from '$lib/i18n';
	import { Button } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import { Download } from 'lucide-svelte';

	interface Props {
		label: string;
		content: string;
		filename: string;
		oncopy: () => void;
	}

	let { label, content, filename, oncopy }: Props = $props();

	function download() {
		const blob = new Blob([content], { type: 'application/json' });
		const url = URL.createObjectURL(blob);
		const a = document.createElement('a');
		a.href = url;
		a.download = filename;
		document.body.appendChild(a);
		a.click();
		document.body.removeChild(a);
		URL.revokeObjectURL(url);
		notifications.success(m.servers_wizard_result_downloaded());
	}
</script>

<div class="code-export-flow">
	<div class="code-export-header">
		<span class="field-label">{label}</span>
		<div class="export-actions">
			<Button size="sm" variant="secondary" onclick={oncopy}>
				{m.common_copy()}
			</Button>
			<Button size="sm" variant="secondary" onclick={download}>
				<Download size={14} class="mr-1" /> {m.common_download()}
			</Button>
		</div>
	</div>
	<pre class="code-preview">{content}</pre>
</div>

<style>
	.field-label {
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--color-text-secondary);
	}

	.code-export-flow {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.code-export-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
	}

	.export-actions {
		display: flex;
		gap: 0.5rem;
	}

	.code-preview {
		margin: 0;
		padding: 0.875rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		font-family: var(--font-mono);
		font-size: 0.6875rem;
		color: var(--color-text-primary);
		overflow-x: auto;
		max-height: 14rem;
	}
</style>
