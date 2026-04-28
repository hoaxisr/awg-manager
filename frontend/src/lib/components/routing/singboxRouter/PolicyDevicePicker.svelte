<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type { RouterPolicyDevice } from '$lib/types';
	import Toggle from '$lib/components/ui/Toggle.svelte';

	interface Props {
		policyName: string;
		onChange: () => Promise<void> | void;
	}
	let { policyName, onChange }: Props = $props();

	let devices = $state<RouterPolicyDevice[]>([]);
	let search = $state('');
	let loading = $state(false);
	let busyMacs = $state<Set<string>>(new Set());

	async function load(): Promise<void> {
		if (!policyName) {
			devices = [];
			return;
		}
		loading = true;
		try {
			devices = await api.singboxRouterPolicyDevices(policyName);
		} catch (e) {
			notifications.error((e as Error).message);
			devices = [];
		} finally {
			loading = false;
		}
	}

	async function toggle(mac: string, bind: boolean): Promise<void> {
		if (busyMacs.has(mac)) return;
		busyMacs = new Set(busyMacs).add(mac);
		try {
			if (bind) await api.singboxRouterPolicyBind(mac, policyName);
			else await api.singboxRouterPolicyUnbind(mac);
			await load();
			await onChange();
		} catch (e) {
			notifications.error((e as Error).message);
		} finally {
			const next = new Set(busyMacs);
			next.delete(mac);
			busyMacs = next;
		}
	}

	$effect(() => {
		if (policyName) load();
	});

	const filtered = $derived(
		devices.filter((d) => {
			const q = search.toLowerCase();
			if (!q) return true;
			return (
				d.name.toLowerCase().includes(q) ||
				d.ip.includes(q) ||
				d.mac.toLowerCase().includes(q)
			);
		})
	);
</script>

<div class="picker">
	<div class="picker-head">
		<span class="title">Устройства в политике</span>
		<input
			type="text"
			class="search"
			placeholder="Поиск по имени, IP, MAC"
			bind:value={search}
		/>
	</div>

	{#if loading}
		<div class="empty">Загрузка устройств…</div>
	{:else if filtered.length === 0}
		<div class="empty">
			{#if devices.length === 0}
				Устройств в hotspot нет. Подключите устройство к роутеру.
			{:else}
				Ничего не найдено по запросу.
			{/if}
		</div>
	{:else}
		<ul class="list">
			{#each filtered as d (d.mac)}
				<li>
					<Toggle
						checked={d.bound}
						onchange={(v) => toggle(d.mac, v)}
						disabled={busyMacs.has(d.mac)}
						size="sm"
					/>
					<div class="meta">
						<div class="name">{d.name || d.mac}</div>
						<div class="sub">{d.ip} · {d.mac}</div>
					</div>
				</li>
			{/each}
		</ul>
	{/if}

	<div class="hint">
		Изменения применяются к новым соединениям. Активные подключения остаются
		на нативном пути до завершения.
	</div>
</div>

<style>
	.picker {
		margin-top: 0.75rem;
		padding: 0.75rem;
		background: var(--bg);
		border: 1px solid var(--border);
		border-radius: 6px;
	}
	.picker-head {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		margin-bottom: 0.5rem;
	}
	.title {
		font-weight: 600;
		font-size: 0.875rem;
	}
	.search {
		flex: 1;
		padding: 0.25rem 0.5rem;
		font-size: 0.85rem;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 4px;
		color: var(--text);
	}
	.empty {
		padding: 0.75rem;
		color: var(--muted-text);
		font-size: 0.85rem;
		text-align: center;
	}
	.list {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.list li {
		display: flex;
		align-items: center;
		gap: 0.625rem;
		padding: 0.375rem 0;
		border-bottom: 1px solid var(--border);
	}
	.list li:last-child {
		border-bottom: none;
	}
	.meta {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	.name {
		font-size: 0.875rem;
		font-weight: 500;
	}
	.sub {
		font-size: 0.75rem;
		font-family: var(--font-mono, ui-monospace, monospace);
		color: var(--muted-text);
	}
	.hint {
		margin-top: 0.625rem;
		font-size: 0.75rem;
		color: var(--muted-text);
	}
</style>
