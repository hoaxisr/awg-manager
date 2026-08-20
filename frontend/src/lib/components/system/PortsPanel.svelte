<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type SystemPortBinding } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { errorMessage } from '$lib/utils/errorMessage';
	import { PortInspectorCard, PortsTableCard, KillProcessModal } from './ports';
	import type { GroupedProcessPort, PortInspectResult, ProtoFilter } from './ports/types';

	let bindings = $state<SystemPortBinding[]>([]);
	let loading = $state(false);
	let busy = $state(false);

	// Quick inspector
	let searchPort = $state('');
	let searchProto = $state<ProtoFilter>('all');
	let inspectedResult = $state<PortInspectResult | null>(null);

	// Table filter
	let tableFilterProto = $state<ProtoFilter>('all');
	let tableSearch = $state('');

	// Kill modal
	let targetGroup = $state<GroupedProcessPort | null>(null);
	let killSignal = $state<'SIGTERM' | 'SIGKILL'>('SIGTERM');

	function groupBindings(list: SystemPortBinding[]): GroupedProcessPort[] {
		const map = new Map<string, GroupedProcessPort>();
		for (const b of list) {
			const key = `${b.port}:${b.pid ?? 0}:${b.processName || b.exe || b.proto}`;
			let g = map.get(key);
			if (!g) {
				g = {
					key,
					port: b.port,
					pid: b.pid,
					processName: b.processName,
					exe: b.exe,
					cmdline: b.cmdline,
					user: b.user,
					service: b.service,
					isSelf: b.isSelf,
					isCritical: b.isCritical,
					protocols: [],
					addresses: [],
					bindings: [],
				};
				map.set(key, g);
			}
			if (!g.protocols.includes(b.proto)) {
				g.protocols.push(b.proto);
			}
			const addrKey = `${b.proto}:${b.ip}:${b.port}`;
			if (!g.addresses.some((a) => `${a.proto}:${a.ip}:${a.port}` === addrKey)) {
				g.addresses.push({ proto: b.proto, ip: b.ip, port: b.port });
			}
			g.bindings.push(b);
		}
		return Array.from(map.values());
	}

	const groupedTableList = $derived.by(() => {
		let list = bindings;
		if (tableFilterProto === 'tcp') {
			list = list.filter((b) => b.proto.startsWith('tcp'));
		} else if (tableFilterProto === 'udp') {
			list = list.filter((b) => b.proto.startsWith('udp'));
		}

		const grouped = groupBindings(list);
		const q = tableSearch.trim().toLowerCase();
		if (!q) return grouped;

		return grouped.filter((g) => {
			if (String(g.port).includes(q)) return true;
			if ((g.processName ?? '').toLowerCase().includes(q)) return true;
			if ((g.cmdline ?? '').toLowerCase().includes(q)) return true;
			if ((g.exe ?? '').toLowerCase().includes(q)) return true;
			if ((g.service ?? '').toLowerCase().includes(q)) return true;
			if (String(g.pid ?? '').includes(q)) return true;
			if (g.addresses.some((a) => a.ip.includes(q))) return true;
			return false;
		});
	});

	onMount(load);

	async function load() {
		loading = true;
		try {
			bindings = await api.systemPortsList();
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось загрузить список портов'));
		} finally {
			loading = false;
		}
	}

	async function handleInspect() {
		const raw = String(searchPort ?? '').trim();
		if (!raw) {
			inspectedResult = null;
			return;
		}
		const p = parseInt(raw, 10);
		if (isNaN(p) || p <= 0 || p > 65535) {
			notifications.error('Укажите корректный номер порта (1–65535)');
			return;
		}

		busy = true;
		try {
			const protoParam = searchProto === 'all' ? undefined : searchProto;
			const res = await api.systemPortsInspect(p, protoParam);
			const items = res.bindings ?? [];
			inspectedResult = {
				searched: true,
				port: p,
				occupied: Boolean(res.occupied && items.length > 0),
				groups: groupBindings(items),
				totalSockets: items.length,
			};
		} catch (e) {
			notifications.error(errorMessage(e, 'Ошибка проверки порта'));
		} finally {
			busy = false;
		}
	}

	function requestKillGroup(group: GroupedProcessPort) {
		targetGroup = group;
		killSignal = 'SIGTERM';
	}

	async function executeKill() {
		if (!targetGroup || !targetGroup.pid) return;
		busy = true;
		const { pid, port, processName } = targetGroup;
		try {
			await api.systemPortsKill({
				pid,
				signal: killSignal,
				port,
			});
			notifications.success(`Процесс ${processName || pid} (PID ${pid}) завершён (${killSignal})`);
			targetGroup = null;
			if (searchPort) {
				await handleInspect();
			}
			await load();
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось завершить процесс'));
		} finally {
			busy = false;
		}
	}
</script>

<div class="ports-panel">
	<PortInspectorCard
		bind:searchPort
		bind:searchProto
		{busy}
		result={inspectedResult}
		oninspect={() => void handleInspect()}
		onkill={requestKillGroup}
	/>

	<PortsTableCard
		{bindings}
		groups={groupedTableList}
		bind:filterProto={tableFilterProto}
		bind:search={tableSearch}
		{loading}
		{busy}
		onrefresh={load}
		onkill={requestKillGroup}
	/>
</div>

<KillProcessModal
	group={targetGroup}
	bind:signal={killSignal}
	{busy}
	onclose={() => (targetGroup = null)}
	onconfirm={executeKill}
/>

<style>
	.ports-panel {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}
</style>
