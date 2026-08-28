<script lang="ts">
	import { Button, Card, Dropdown, type DropdownOption } from '$lib/components/ui';
	import { Search, Power, CheckCircle2, AlertTriangle } from 'lucide-svelte';
	import PortAddressPills from './PortAddressPills.svelte';
	import PortProcessBadges from './PortProcessBadges.svelte';
	import type { GroupedProcessPort, PortInspectResult, ProtoFilter } from './types';

	interface Props {
		searchPort: string;
		searchProto: ProtoFilter;
		busy: boolean;
		result: PortInspectResult | null;
		oninspect: () => void;
		onkill: (group: GroupedProcessPort) => void;
	}

	let {
		searchPort = $bindable(),
		searchProto = $bindable(),
		busy,
		result,
		oninspect,
		onkill,
	}: Props = $props();

	const inspectProtoOptions: DropdownOption<ProtoFilter>[] = [
		{ value: 'all', label: 'Любой протокол (TCP/UDP)' },
		{ value: 'tcp', label: 'Только TCP' },
		{ value: 'udp', label: 'Только UDP' },
	];
</script>

<Card padding="sm">
	<div class="card-header">
		<div>
			<h3>Проверка и освобождение порта</h3>
			<p class="subtitle">Введите номер порта, чтобы узнать, какой процесс его слушает, и освободить его при необходимости</p>
		</div>
	</div>

	<form class="inspect-form" onsubmit={(e) => { e.preventDefault(); oninspect(); }}>
		<div class="input-wrap">
			<input
				type="text"
				inputmode="numeric"
				placeholder="Номер порта (например, 56013, 2222, 8080)"
				bind:value={searchPort}
				onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); oninspect(); } }}
			/>
		</div>
		<div class="proto-dropdown">
			<Dropdown
				value={searchProto}
				options={inspectProtoOptions}
				onchange={(v) => { searchProto = v as ProtoFilter; if (searchPort) oninspect(); }}
			/>
		</div>
		<Button type="submit" variant="primary" loading={busy} onclick={oninspect}>
			{#snippet iconBefore()}<Search size={15} />{/snippet}
			Проверить порт
		</Button>
	</form>

	{#if result}
		<div class="inspect-result">
			{#if !result.occupied}
				<div class="result-box free">
					<CheckCircle2 size={20} class="icon-free" />
					<div>
						<div class="res-title">Порт {result.port} свободен</div>
						<div class="res-desc">Ни один процесс в данный момент не слушает этот порт.</div>
					</div>
				</div>
			{:else}
				<div class="result-box occupied">
					<div class="occupied-header">
						<AlertTriangle size={20} class="icon-occupied" />
						<div class="res-title">
							Порт {result.port} занят ({result.groups.length} {result.groups.length === 1 ? 'процесс' : 'процесса'}, {result.totalSockets} {result.totalSockets === 1 ? 'сокет' : 'сокета'})
						</div>
					</div>
					<div class="occupied-list">
						{#each result.groups as group (group.key)}
							<div class="occupied-item">
								<div class="item-main">
									<div class="item-line">
										<span class="proc-name"><strong>{group.processName || 'Процесс без имени'}</strong></span>
										{#if group.pid}
											<span class="pid-badge">PID: {group.pid}</span>
										{/if}
										<PortProcessBadges
											service={group.service}
											isSelf={group.isSelf}
											isCritical={group.isCritical}
											selfLabel="awg-manager"
											selfTitle="Текущий сервер awg-manager"
											criticalTitle="Системный процесс роутера"
										/>
									</div>

									<PortAddressPills addresses={group.addresses} label="Адреса привязки:" />

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
											onclick={() => onkill(group)}
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

<style>
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
	.pid-badge {
		font-size: 0.72rem;
		padding: 0.1rem 0.35rem;
		border-radius: 4px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
	}

	.item-sub {
		font-size: 0.78rem;
		color: var(--color-text-secondary);
		margin-top: 0.25rem;
	}
	.item-sub.cmd code {
		word-break: break-all;
	}

	@media (max-width: 768px) {
		.inspect-form {
			flex-direction: column;
			align-items: stretch;
		}
		.occupied-item {
			flex-direction: column;
			align-items: stretch;
		}
	}
</style>
