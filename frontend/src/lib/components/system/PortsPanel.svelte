<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type SystemPortBinding } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Card, Modal, Dropdown, SegmentedControl, type DropdownOption } from '$lib/components/ui';
	import { errorMessage } from '$lib/utils/errorMessage';
	import { RefreshCw, Search, ShieldAlert, Power, CheckCircle2, AlertTriangle } from 'lucide-svelte';

	type ProtoFilter = 'all' | 'tcp' | 'udp';

	interface GroupedProcessPort {
		key: string;
		port: number;
		pid?: number;
		processName?: string;
		exe?: string;
		cmdline?: string;
		user?: string;
		service?: string;
		isSelf?: boolean;
		isCritical?: boolean;
		protocols: string[];
		addresses: { proto: string; ip: string; port: number }[];
		bindings: SystemPortBinding[];
	}

	let bindings = $state<SystemPortBinding[]>([]);
	let loading = $state(false);
	let busy = $state(false);

	// Quick inspector
	let searchPort = $state('');
	let searchProto = $state<ProtoFilter>('all');
	let inspectedResult = $state<{
		searched: boolean;
		port: number;
		occupied: boolean;
		groups: GroupedProcessPort[];
		totalSockets: number;
	} | null>(null);

	// Table filter
	let tableFilterProto = $state<ProtoFilter>('all');
	let tableSearch = $state('');

	// Kill modal
	let targetGroup = $state<GroupedProcessPort | null>(null);
	let killSignal = $state<'SIGTERM' | 'SIGKILL'>('SIGTERM');

	const inspectProtoOptions: DropdownOption<ProtoFilter>[] = [
		{ value: 'all', label: 'Любой протокол (TCP/UDP)' },
		{ value: 'tcp', label: 'Только TCP' },
		{ value: 'udp', label: 'Только UDP' },
	];

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

	const tcpCount = $derived(bindings.filter((b) => b.proto.startsWith('tcp')).length);
	const udpCount = $derived(bindings.filter((b) => b.proto.startsWith('udp')).length);

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
	<!-- 1. Quick Port Inspector Card -->
	<Card padding="sm">
		<div class="card-header">
			<div>
				<h3>Проверка и освобождение порта</h3>
				<p class="subtitle">Введите номер порта, чтобы узнать, какой процесс его слушает, и освободить его при необходимости</p>
			</div>
		</div>

		<form class="inspect-form" onsubmit={(e) => { e.preventDefault(); void handleInspect(); }}>
			<div class="input-wrap">
				<input
					type="text"
					inputmode="numeric"
					placeholder="Номер порта (например, 56013, 2222, 8080)"
					bind:value={searchPort}
					onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); void handleInspect(); } }}
				/>
			</div>
			<div class="proto-dropdown">
				<Dropdown
					value={searchProto}
					options={inspectProtoOptions}
					onchange={(v) => { searchProto = v as ProtoFilter; if (searchPort) void handleInspect(); }}
				/>
			</div>
			<Button type="submit" variant="primary" loading={busy} onclick={handleInspect}>
				{#snippet iconBefore()}<Search size={15} />{/snippet}
				Проверить порт
			</Button>
		</form>

		{#if inspectedResult}
			<div class="inspect-result">
				{#if !inspectedResult.occupied}
					<div class="result-box free">
						<CheckCircle2 size={20} class="icon-free" />
						<div>
							<div class="res-title">Порт {inspectedResult.port} свободен</div>
							<div class="res-desc">Ни один процесс в данный момент не слушает этот порт.</div>
						</div>
					</div>
				{:else}
					<div class="result-box occupied">
						<div class="occupied-header">
							<AlertTriangle size={20} class="icon-occupied" />
							<div class="res-title">
								Порт {inspectedResult.port} занят ({inspectedResult.groups.length} {inspectedResult.groups.length === 1 ? 'процесс' : 'процесса'}, {inspectedResult.totalSockets} {inspectedResult.totalSockets === 1 ? 'сокет' : 'сокета'})
							</div>
						</div>
						<div class="occupied-list">
							{#each inspectedResult.groups as group (group.key)}
								<div class="occupied-item">
									<div class="item-main">
										<div class="item-line">
											<span class="proc-name"><strong>{group.processName || 'Процесс без имени'}</strong></span>
											{#if group.pid}
												<span class="pid-badge">PID: {group.pid}</span>
											{/if}
											{#if group.service}
												<span class="svc-badge" title="Служба init.d">{group.service}</span>
											{/if}
											{#if group.isSelf}
												<span class="self-badge" title="Текущий сервер awg-manager">awg-manager</span>
											{/if}
											{#if group.isCritical}
												<span class="crit-badge" title="Системный процесс роутера"><ShieldAlert size={12} /></span>
											{/if}
										</div>

										<div class="addr-chips">
											<span class="addr-label">Адреса привязки:</span>
											{#each group.addresses as a}
												<span class="addr-pill">
													<span class="pill-proto {a.proto.startsWith('udp') ? 'udp' : 'tcp'}">{a.proto.toUpperCase()}</span>
													<code>{a.ip}:{a.port}</code>
												</span>
											{/each}
										</div>

										{#if group.exe}
											<div class="item-sub"><strong>Бинарник:</strong> <code>{group.exe}</code></div>
										{/if}
										{#if group.cmdline}
											<div class="item-sub cmd"><strong>Команда:</strong> <code>{group.cmdline}</code></div>
										{/if}
									</div>
									<div class="item-act">
										{#if group.pid}
											<Button
												size="sm"
												variant={group.isCritical || group.isSelf ? 'outline-danger' : 'danger'}
												onclick={() => requestKillGroup(group)}
											>
												{#snippet iconBefore()}<Power size={14} />{/snippet}
												Освободить порт
											</Button>
										{:else}
											<span class="no-pid-hint">Ядро / без PID</span>
										{/if}
									</div>
								</div>
							{/each}
						</div>
					</div>
				{/if}
			</div>
		{/if}
	</Card>

	<!-- 2. Listening Ports Table (Grouped by Process) -->
	<Card padding="sm">
		<div class="table-header">
			<div class="left-controls">
				<h3>Открытые порты системы ({groupedTableList.length} процессов, {bindings.length} сокетов)</h3>
				<SegmentedControl
					value={tableFilterProto}
					options={[
						{ value: 'all', label: `Все (${bindings.length})` },
						{ value: 'tcp', label: `TCP (${tcpCount})` },
						{ value: 'udp', label: `UDP (${udpCount})` },
					]}
					ariaLabel="Фильтр по протоколу"
					onchange={(v) => (tableFilterProto = v as ProtoFilter)}
				/>
			</div>
			<div class="right-controls">
				<input
					type="text"
					placeholder="Поиск по порту, процессу, IP…"
					bind:value={tableSearch}
					class="table-search-input"
				/>
				<Button variant="ghost" onclick={load} disabled={loading}>
					{#snippet iconBefore()}<RefreshCw size={14} />{/snippet}
					Обновить
				</Button>
			</div>
		</div>

		{#if loading && bindings.length === 0}
			<p class="muted">Сканирование портов…</p>
		{:else if groupedTableList.length === 0}
			<p class="muted">Порты не найдены</p>
		{:else}
			<div class="table-wrap">
				<table class="ports-table">
					<thead>
						<tr>
							<th style="width: 90px;">Протокол</th>
							<th style="width: 80px;">Порт</th>
							<th style="width: 220px;">Адреса привязки</th>
							<th>Процесс / Служба</th>
							<th style="width: 85px;">PID</th>
							<th style="width: 130px; text-align: right;">Действие</th>
						</tr>
					</thead>
					<tbody>
						{#each groupedTableList as g (g.key)}
							<tr class:self-row={g.isSelf}>
								<td>
									<div class="proto-list">
										{#each g.protocols as proto}
											<span class="proto-badge {proto.startsWith('udp') ? 'udp' : 'tcp'}">
												{proto.toUpperCase()}
											</span>
										{/each}
									</div>
								</td>
								<td>
									<span class="port-num">{g.port}</span>
								</td>
								<td>
									<div class="addr-list">
										{#each g.addresses as addr}
											<code class="addr-item">{addr.ip}</code>
										{/each}
									</div>
								</td>
								<td>
									<div class="proc-cell">
										<div class="proc-title">
											<strong>{g.processName || '—'}</strong>
											{#if g.service}
												<span class="svc-badge" title="Служба init.d">{g.service}</span>
											{/if}
											{#if g.isSelf}
												<span class="self-badge" title="Текущий веб-сервер">текущий</span>
											{/if}
											{#if g.isCritical}
												<span class="crit-badge" title="Системный процесс"><ShieldAlert size={12} /></span>
											{/if}
										</div>
										{#if g.cmdline}
											<div class="proc-cmd" title={g.cmdline}>{g.cmdline}</div>
										{:else if g.exe}
											<div class="proc-cmd" title={g.exe}>{g.exe}</div>
										{/if}
									</div>
								</td>
								<td>
									{#if g.pid}
										<code>{g.pid}</code>
									{:else}
										<span class="muted">—</span>
									{/if}
								</td>
								<td class="act-cell">
									{#if g.pid}
										<Button
											size="sm"
											variant={g.isCritical || g.isSelf ? 'secondary' : 'outline-danger'}
											disabled={busy}
											onclick={() => requestKillGroup(g)}
										>
											{#snippet iconBefore()}<Power size={13} />{/snippet}
											Освободить
										</Button>
									{:else}
										<span class="muted">—</span>
									{/if}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
	</Card>
</div>

<!-- 3. Safe Confirmation Modal -->
<Modal
	open={!!targetGroup}
	title={`Освободить порт ${targetGroup?.port ?? ''}?`}
	size="md"
	onclose={() => (targetGroup = null)}
>
	{#if targetGroup}
		<div class="modal-body">
			<p>
				Вы действительно хотите завершить процесс <strong>{targetGroup.processName || 'без имени'}</strong> (PID: <code>{targetGroup.pid}</code>), занимающий порт <strong>{targetGroup.port}</strong>?
			</p>

			<div class="modal-addrs">
				<strong>Будут освобождены адреса:</strong>
				<div class="addr-chips" style="margin-top: 0.3rem;">
					{#each targetGroup.addresses as a}
						<span class="addr-pill">
							<span class="pill-proto {a.proto.startsWith('udp') ? 'udp' : 'tcp'}">{a.proto.toUpperCase()}</span>
							<code>{a.ip}:{a.port}</code>
						</span>
					{/each}
				</div>
			</div>

			{#if targetGroup.isSelf}
				<div class="modal-alert danger">
					<AlertTriangle size={18} />
					<div>
						<strong>Внимание!</strong> Это процесс текущего сервера <code>awg-manager</code>. Завершение немедленно прервёт работу веб-интерфейса.
					</div>
				</div>
			{:else if targetGroup.isCritical}
				<div class="modal-alert warning">
					<AlertTriangle size={18} />
					<div>
						<strong>Внимание!</strong> Этот процесс (<code>{targetGroup.processName}</code>) является системным (SSH/NDM). Завершение может нарушить доступ к роутеру.
					</div>
				</div>
			{/if}

			{#if targetGroup.cmdline}
				<div class="cmd-box">
					<div class="cmd-label">Команда запуска:</div>
					<code>{targetGroup.cmdline}</code>
				</div>
			{/if}

			<div class="signal-selector">
				<span class="sig-label">Тип сигнала:</span>
				<label class="sig-option">
					<input type="radio" name="killSignal" value="SIGTERM" bind:group={killSignal} />
					<span><strong>SIGTERM</strong> (Мягкое завершение процесса, рекомендуется)</span>
				</label>
				<label class="sig-option">
					<input type="radio" name="killSignal" value="SIGKILL" bind:group={killSignal} />
					<span><strong>SIGKILL</strong> (Принудительное немедленное убийство процесса)</span>
				</label>
			</div>
		</div>
	{/if}

	{#snippet actions()}
		<div class="modal-footer-btns">
			<Button variant="ghost" onclick={() => (targetGroup = null)}>Отмена</Button>
			<Button variant="danger" loading={busy} onclick={executeKill}>
				Завершить ({killSignal})
			</Button>
		</div>
	{/snippet}
</Modal>

<style>
	.ports-panel {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.card-header h3 {
		margin: 0;
		font-size: 1rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.subtitle {
		margin: 0.2rem 0 0.75rem 0;
		font-size: 0.82rem;
		color: var(--color-text-muted);
	}

	.inspect-form {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		align-items: center;
	}
	.input-wrap {
		flex: 1;
		min-width: 220px;
	}
	.input-wrap input {
		width: 100%;
		padding: 0.45rem 0.65rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		font-size: 0.9rem;
	}
	.input-wrap input:focus {
		border-color: var(--color-accent);
		outline: none;
	}
	.proto-dropdown {
		min-width: 220px;
	}

	.inspect-result {
		margin-top: 0.85rem;
	}
	.result-box {
		padding: 0.75rem 1rem;
		border-radius: var(--radius-md, 8px);
		display: flex;
		gap: 0.75rem;
		align-items: flex-start;
	}
	.result-box.free {
		background: var(--color-success-tint, rgba(34, 197, 94, 0.1));
		border: 1px solid var(--color-success-border, rgba(34, 197, 94, 0.3));
	}
	.result-box.occupied {
		background: var(--color-error-tint, rgba(239, 68, 68, 0.08));
		border: 1px solid var(--color-error-border, rgba(239, 68, 68, 0.25));
		flex-direction: column;
	}
	.occupied-header {
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}
	:global(.icon-free) {
		color: var(--color-success, #22c55e);
		flex-shrink: 0;
	}
	:global(.icon-occupied) {
		color: var(--color-error, #ef4444);
		flex-shrink: 0;
	}
	.res-title {
		font-weight: 600;
		font-size: 0.95rem;
		color: var(--color-text-primary);
	}
	.res-desc {
		font-size: 0.82rem;
		color: var(--color-text-secondary);
		margin-top: 0.15rem;
	}

	.occupied-list {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		width: 100%;
		margin-top: 0.5rem;
	}
	.occupied-item {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: 0.75rem;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm, 6px);
		gap: 0.75rem;
	}
	.item-line {
		display: flex;
		flex-wrap: wrap;
		gap: 0.4rem;
		align-items: center;
		font-size: 0.95rem;
	}
	.proc-name {
		font-size: 0.95rem;
		color: var(--color-text-primary);
	}

	.addr-chips {
		display: flex;
		flex-wrap: wrap;
		gap: 0.4rem;
		align-items: center;
		margin-top: 0.35rem;
	}
	.addr-label {
		font-size: 0.78rem;
		color: var(--color-text-muted);
		font-weight: 500;
	}
	.addr-pill {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		padding: 0.15rem 0.45rem;
		border-radius: 4px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		font-size: 0.8rem;
	}
	.pill-proto {
		font-size: 0.65rem;
		font-weight: 700;
		padding: 0.05rem 0.25rem;
		border-radius: 3px;
	}
	.pill-proto.tcp {
		background: var(--color-accent-tint, rgba(59, 130, 246, 0.2));
		color: var(--color-accent, #60a5fa);
	}
	.pill-proto.udp {
		background: rgba(168, 85, 247, 0.2);
		color: #c084fc;
	}

	.item-sub {
		font-size: 0.78rem;
		color: var(--color-text-secondary);
		margin-top: 0.25rem;
	}
	.item-sub.cmd code {
		word-break: break-all;
	}

	.proto-list {
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
	}
	.proto-badge {
		font-size: 0.7rem;
		font-weight: 700;
		padding: 0.15rem 0.4rem;
		border-radius: 4px;
		letter-spacing: 0.03em;
		display: inline-block;
		text-align: center;
	}
	.proto-badge.tcp {
		background: var(--color-accent-tint, rgba(59, 130, 246, 0.2));
		color: var(--color-accent, #60a5fa);
		border: 1px solid var(--color-accent-border, rgba(59, 130, 246, 0.4));
	}
	.proto-badge.udp {
		background: rgba(168, 85, 247, 0.18);
		color: #c084fc;
		border: 1px solid rgba(168, 85, 247, 0.35);
	}

	.pid-badge, .svc-badge, .self-badge, .crit-badge {
		font-size: 0.72rem;
		padding: 0.1rem 0.35rem;
		border-radius: 4px;
	}
	.pid-badge {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
	}
	.svc-badge {
		background: var(--color-success-tint, rgba(16, 185, 129, 0.15));
		color: var(--color-success, #34d399);
	}
	.self-badge {
		background: var(--color-warning-tint, rgba(234, 179, 8, 0.15));
		color: var(--color-warning, #facc15);
	}
	.crit-badge {
		background: var(--color-error-tint, rgba(239, 68, 68, 0.15));
		color: var(--color-error, #f87171);
		display: inline-flex;
		align-items: center;
	}

	.port-num {
		font-weight: 700;
		font-size: 0.95rem;
		font-family: var(--font-mono, monospace);
		color: var(--color-text-primary);
	}

	.addr-list {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
	}
	.addr-item {
		font-size: 0.8rem;
		color: var(--color-text-secondary);
	}

	.table-header {
		display: flex;
		flex-wrap: wrap;
		gap: 0.75rem;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 0.75rem;
	}
	.left-controls, .right-controls {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		align-items: center;
	}
	.table-header h3 {
		margin: 0;
		font-size: 0.95rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.table-search-input {
		padding: 0.35rem 0.55rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		font-size: 0.82rem;
		min-width: 180px;
	}
	.table-search-input:focus {
		border-color: var(--color-accent);
		outline: none;
	}

	.table-wrap {
		max-height: 480px;
		overflow-y: auto;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm, 6px);
		background: var(--color-bg-secondary);
	}
	.ports-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.85rem;
	}
	.ports-table th, .ports-table td {
		padding: 0.5rem 0.65rem;
		border-bottom: 1px solid var(--color-border);
		text-align: left;
		vertical-align: middle;
	}
	.ports-table th {
		position: sticky;
		top: 0;
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
		font-size: 0.75rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		z-index: 1;
	}
	.proc-cell {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
		max-width: 320px;
	}
	.proc-title {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.35rem;
		color: var(--color-text-primary);
	}
	.proc-cmd {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.act-cell {
		text-align: right;
	}

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

	.muted {
		color: var(--color-text-muted);
	}

	@media (max-width: 768px) {
		.inspect-form {
			flex-direction: column;
			align-items: stretch;
		}
		.table-header {
			flex-direction: column;
			align-items: stretch;
		}
		.occupied-item {
			flex-direction: column;
			align-items: stretch;
		}
	}
</style>
