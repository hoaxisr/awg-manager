<!-- frontend/src/lib/components/routing/singboxRouter/ConnectionsBulkBar.svelte -->
<script lang="ts">
	import { m } from '$lib/i18n';
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
		const counts = new Map<string, number>();
		for (const c of visible) {
			const k = c.outboundLabel;
			counts.set(k, (counts.get(k) ?? 0) + 1);
		}
		return Array.from(counts.entries()).sort((a, b) => b[1] - a[1]);
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
			{m.routing_singbox_conn_visible()} <strong>{visible.length}</strong> {m.routing_singbox_conn_visible_of({ total })}
			<span class="dot">·</span>
			↑ {formatBytes(totalUp)} · ↓ {formatBytes(totalDown)}
		</span>
		<button class="kill-btn" type="button" onclick={() => (showModal = true)}>{m.routing_singbox_conn_close_visible()}</button>
	</div>
{/if}

{#if showModal}
	{@const outboundsLine = outboundCounts.map(([k, n]) => `${k} (${n})`).join(', ')}
	<ConfirmModal
		open={showModal}
		title={m.routing_singbox_conn_confirm_title()}
		message={m.routing_singbox_conn_confirm_message({ count: visible.length })}
		secondary={m.routing_singbox_conn_confirm_secondary({ outbounds: outboundsLine })}
		confirmLabel={m.routing_singbox_conn_close_all()}
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
		background: var(--bg-secondary, #16161e);
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
		color: #ff6b6b;
		font-size: 12px;
	}
	.kill-btn:hover { background: rgba(255, 107, 107, 0.2); }
</style>
