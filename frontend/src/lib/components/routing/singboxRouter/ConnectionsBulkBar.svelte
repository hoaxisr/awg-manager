<!-- frontend/src/lib/components/routing/singboxRouter/ConnectionsBulkBar.svelte -->
<script lang="ts">
	import type { Connection } from '$lib/types/singboxConnections';
	import { formatBytes } from '$lib/utils/format';
	import { ConfirmModal } from '$lib/components/ui';

	interface Props {
		visible: Connection[];
		total: number;
		onConfirmKill: () => Promise<void>;
	}

	let { visible, total, onConfirmKill }: Props = $props();

	let showModal = $state(false);
	let busy = $state(false);

	const totalUp = $derived(visible.reduce((s, c) => s + c.upload, 0));
	const totalDown = $derived(visible.reduce((s, c) => s + c.download, 0));

	const outboundCounts = $derived.by(() => {
		const m = new Map<string, number>();
		for (const c of visible) {
			const k = c.outboundLabel;
			m.set(k, (m.get(k) ?? 0) + 1);
		}
		return Array.from(m.entries()).sort((a, b) => b[1] - a[1]);
	});

	async function confirm(): Promise<void> {
		busy = true;
		try {
			await onConfirmKill();
		} finally {
			busy = false;
			showModal = false;
		}
	}
</script>

{#if visible.length > 0 && visible.length < total}
	<div class="bar">
		<span>
			Видимо: <strong>{visible.length}</strong> из {total}
			<span class="dot">·</span>
			↑ {formatBytes(totalUp)} · ↓ {formatBytes(totalDown)}
		</span>
		<button class="kill-btn" type="button" onclick={() => (showModal = true)}>Закрыть видимые</button>
	</div>
{/if}

{#if showModal}
	{@const outboundsLine = outboundCounts.map(([k, n]) => `${k} (${n})`).join(', ')}
	<ConfirmModal
		open={showModal}
		title="Подтверждение"
		message={`Закрыть ${visible.length} соединений?`}
		secondary={`Затронутые outbound'ы: ${outboundsLine}. Это действие необратимо — клиенты переподключатся.`}
		confirmLabel="Закрыть всё"
		variant="danger"
		{busy}
		onConfirm={confirm}
		onClose={() => (showModal = false)}
	/>
{/if}

<style>
	.bar {
		display: flex; justify-content: space-between; align-items: center;
		padding: 8px 12px; margin-bottom: 8px;
		background: var(--bg-secondary);
		border: 1px solid var(--border, #3b4261);
		border-radius: 6px;
		font-size: 13px;
	}
	.dot { color: var(--text-tertiary, #6e6e6e); margin: 0 6px; }
	.kill-btn {
		all: unset; cursor: pointer;
		padding: 4px 12px;
		border-radius: 4px;
		background: rgba(255, 107, 107, 0.1);
		color: var(--color-error);
		font-size: 12px;
	}
	.kill-btn:hover { background: rgba(255, 107, 107, 0.2); }
</style>
