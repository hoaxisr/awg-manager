<script lang="ts">
	import type { PolicyDevice } from '$lib/types';
	import { Badge } from '$lib/components/ui';
	import { ArrowLeft } from 'lucide-svelte';

	interface Props {
		devices: PolicyDevice[];
		currentPolicy: string;
		onassign: (mac: string) => void;
	}

	let { devices, currentPolicy, onassign }: Props = $props();

	let search = $state('');

	let filtered = $derived.by(() => {
		const visible = devices.filter((d) => d.policy !== currentPolicy);
		if (!search.trim()) return visible;
		const q = search.trim().toLowerCase();
		return visible.filter(
			(d) =>
				d.name.toLowerCase().includes(q) ||
				d.hostname.toLowerCase().includes(q) ||
				d.ip.toLowerCase().includes(q)
		);
	});
</script>

<div class="device-list-section">
	<h4 class="section-title">Все устройства</h4>

	<input
		type="text"
		class="search-input"
		placeholder="Поиск по имени, хосту, IP..."
		bind:value={search}
	/>

	<div class="device-scroll">
		{#each filtered as device}
			{@const isActive = device.active && device.link === 'up'}
			{@const isBusy = device.policy !== '' && device.policy !== currentPolicy}
			<div
				class="device-row"
				class:dimmed={isBusy}
				role="listitem"
				draggable={!isBusy}
				ondragstart={(e) => {
					if (isBusy) return;
					e.dataTransfer?.setData('text/plain', device.mac);
				}}
			>
				<span class="led" class:led-green={isActive} class:led-gray={!isActive}></span>
				<div class="device-info">
					<span class="device-name">{device.name || device.hostname || device.mac}</span>
					{#if device.ip}
						<span class="device-ip">{device.ip}</span>
					{/if}
				</div>
				{#if isBusy}
					<Badge variant="muted" size="xs">{device.policy}</Badge>
				{/if}
				<button
					class="assign-btn"
					title="Назначить в политику"
					disabled={isBusy}
					onclick={() => { if (!isBusy) onassign(device.mac); }}
				>
					<ArrowLeft size={15} />
				</button>
			</div>
		{/each}
		{#if filtered.length === 0}
			<p class="empty-text">Нет устройств</p>
		{/if}
	</div>
</div>

<style>
	.device-list-section {
		display: flex;
		flex-direction: column;
		gap: 8px;
		flex: 1;
		min-height: 0;
		overflow: hidden;
	}

	.section-title {
		font-size: 0.8125rem;
		font-weight: 600;
		margin: 0;
		color: var(--text-primary);
	}

	.search-input {
		width: 100%;
		padding: 7px 10px;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--bg-secondary);
		color: var(--text-primary);
		font-size: 0.8125rem;
		outline: none;
		transition: border-color 0.15s;
	}

	.search-input:focus {
		border-color: var(--accent);
	}

	.device-scroll {
		flex: 1;
		min-height: 0;
		overflow-y: auto;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.device-row {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 6px 10px;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--bg-secondary);
		cursor: grab;
		transition: opacity 0.15s;
	}

	.device-row:active {
		cursor: grabbing;
	}

	.device-row.dimmed {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.device-info {
		flex: 1;
		display: flex;
		flex-direction: column;
		min-width: 0;
	}

	.device-name {
		font-size: 0.8125rem;
		font-weight: 500;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.device-ip {
		font-size: 0.6875rem;
		color: var(--text-muted);
	}

	.assign-btn {
		display: flex;
		padding: 3px;
		background: none;
		border: none;
		color: var(--border-hover);
		cursor: pointer;
		border-radius: 4px;
		transition: color 0.15s;
		flex-shrink: 0;
	}

	.assign-btn:hover {
		color: var(--accent);
	}

	.assign-btn:disabled {
		opacity: 0.3;
		cursor: not-allowed;
	}

	.led {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.led-green {
		background: var(--success);
		box-shadow: 0 0 6px var(--success);
	}

	.led-gray {
		background: var(--text-muted);
	}

	.empty-text {
		font-size: 0.8125rem;
		color: var(--text-muted);
		text-align: center;
		margin: 12px 0;
	}
</style>
