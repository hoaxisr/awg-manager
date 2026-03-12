<!-- frontend/src/lib/components/ExternalTunnelCard.svelte -->
<script lang="ts">
	import type { ExternalTunnel } from '$lib/types';
	import { formatBytes } from '$lib/utils/format';

	interface Props {
		tunnel: ExternalTunnel;
		onadopt?: (interfaceName: string) => void;
	}

	let { tunnel, onadopt }: Props = $props();

	function handleAdopt(): void {
		onadopt?.(tunnel.interfaceName);
	}
</script>

<div class="card flex flex-col gap-4 border-2 border-dashed border-warning-500/30">
	<div class="flex justify-between items-start gap-3">
		<div class="flex flex-col gap-1">
			<div class="flex items-center gap-2">
				<h3 class="text-lg font-semibold">{tunnel.interfaceName}</h3>
				<span
					class="inline-flex items-center gap-1.5 px-2.5 py-0.5 text-xs font-medium rounded-full bg-warning-500/15 text-warning-500"
				>
					Внешний
				</span>
			</div>
			<span class="text-xs text-surface-400 font-mono">AWG туннель</span>
		</div>
		<div>
			{#if tunnel.lastHandshake}
				<span
					class="inline-flex items-center gap-1.5 px-2.5 py-0.5 text-xs font-medium rounded-full bg-success-500/15 text-success-500"
				>
					<span class="w-1.5 h-1.5 rounded-full bg-current"></span>
					Подключён
				</span>
			{:else}
				<span
					class="inline-flex items-center gap-1.5 px-2.5 py-0.5 text-xs font-medium rounded-full bg-surface-400/15 text-surface-400"
				>
					<span class="w-1.5 h-1.5 rounded-full bg-current"></span>
					Неактивен
				</span>
			{/if}
		</div>
	</div>

	<div class="flex flex-col gap-2 pt-3 border-t border-surface-300-700">
		{#if tunnel.endpoint}
			<div class="flex gap-4">
				<div class="flex flex-col gap-0.5 min-w-0">
					<span class="text-xs text-surface-400 uppercase">Endpoint</span>
					<span class="text-sm font-mono">{tunnel.endpoint}</span>
				</div>
			</div>
		{/if}
		{#if tunnel.lastHandshake}
			<div class="flex gap-4">
				<div class="flex flex-col gap-0.5 min-w-0">
					<span class="text-xs text-surface-400 uppercase">Handshake</span>
					<span class="text-sm font-mono">{tunnel.lastHandshake}</span>
				</div>
			</div>
		{/if}
		<div class="flex gap-4">
			<div class="flex flex-col gap-0.5 min-w-0">
				<span class="text-xs text-surface-400 uppercase">RX</span>
				<span class="text-sm font-mono">{formatBytes(tunnel.rxBytes)}</span>
			</div>
			<div class="flex flex-col gap-0.5 min-w-0">
				<span class="text-xs text-surface-400 uppercase">TX</span>
				<span class="text-sm font-mono">{formatBytes(tunnel.txBytes)}</span>
			</div>
		</div>
	</div>

	<div class="pt-3 border-t border-surface-300-700">
		<button class="btn btn-primary" onclick={handleAdopt}>
			<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
				<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/>
				<polyline points="9 12 12 15 16 10"/>
			</svg>
			Взять под управление
		</button>
	</div>
</div>
