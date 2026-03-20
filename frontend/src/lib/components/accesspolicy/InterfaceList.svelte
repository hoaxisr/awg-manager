<script lang="ts">
	import type { AccessPolicyInterface, PolicyGlobalInterface } from '$lib/types';
	import { Modal } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';

	interface Props {
		interfaces: AccessPolicyInterface[];
		availableInterfaces: PolicyGlobalInterface[];
		onpermit: (iface: string, order: number) => void;
		ondeny: (iface: string) => void;
		onreorder: (iface: string, newOrder: number) => void;
		onupdate: () => void;
	}

	let { interfaces: rawInterfaces, availableInterfaces, onpermit, ondeny, onreorder, onupdate }: Props = $props();

	let interfaces = $derived(rawInterfaces ?? []);
	let showAdd = $state(false);

	let sorted = $derived([...interfaces].sort((a, b) => a.order - b.order));

	let unassigned = $derived(
		(availableInterfaces ?? []).filter(
			(gi) => !interfaces.some((i) => i.name === gi.name)
		)
	);

	function handleAdd(iface: string) {
		onpermit(iface, interfaces.length);
		showAdd = false;
	}

	function getLabel(name: string): string {
		const gi = (availableInterfaces ?? []).find(g => g.name === name);
		return gi?.label || name;
	}

	function isUp(name: string): boolean {
		return (availableInterfaces ?? []).find(gi => gi.name === name)?.up ?? false;
	}

	let toggling = $state('');
	let confirmToggle = $state<{ name: string; label: string; currentlyUp: boolean } | null>(null);

	function requestToggle(name: string) {
		confirmToggle = {
			name,
			label: getLabel(name),
			currentlyUp: isUp(name),
		};
	}

	async function executeToggle() {
		if (!confirmToggle) return;
		const { name, currentlyUp } = confirmToggle;
		confirmToggle = null;
		toggling = name;
		try {
			await api.setPolicyInterfaceUp(name, !currentlyUp);
			onupdate();
		} catch (e: any) {
			notifications.error(`Ошибка: ${e.message}`);
		} finally {
			toggling = '';
		}
	}

	function moveUp(index: number) {
		if (index <= 0) return;
		onreorder(sorted[index].name, sorted[index - 1].order);
	}

	function moveDown(index: number) {
		if (index >= sorted.length - 1) return;
		onreorder(sorted[index].name, sorted[index + 1].order);
	}
</script>

<div class="iface-section">
	<div class="section-header">
		<h4>Интерфейсы (приоритет)</h4>
		{#if unassigned.length > 0}
			<button class="link-btn" onclick={() => (showAdd = !showAdd)}>
				{showAdd ? 'Отмена' : 'Добавить'}
			</button>
		{/if}
	</div>

	{#if showAdd}
		<div class="add-dropdown">
			{#each unassigned as gi}
				<button class="dropdown-item" onclick={() => handleAdd(gi.name)}>
					<span class="iface-name">{gi.label || gi.name}</span>
					{#if !gi.up}
						<span class="iface-down">down</span>
					{/if}
				</button>
			{/each}
		</div>
	{/if}

	{#if sorted.length === 0}
		<p class="empty-text">Нет разрешённых интерфейсов</p>
	{:else}
		<div class="iface-list">
			{#each sorted as iface, index}
				<div class="iface-row" class:denied={iface.denied}>
					<span class="iface-order">{index + 1}</span>
					<span class="led" class:led-green={!iface.denied && isUp(iface.name)} class:led-gray={iface.denied || !isUp(iface.name)}></span>
					<span class="iface-label" title={iface.name}>{getLabel(iface.name)}</span>
					{#if iface.denied}
						<span class="denied-badge">запрещён</span>
					{/if}
					<button
						class="icon-btn"
						class:active-toggle={isUp(iface.name)}
						title={isUp(iface.name) ? 'Выключить интерфейс' : 'Включить интерфейс'}
						disabled={toggling === iface.name}
						onclick={() => requestToggle(iface.name)}
					>
						<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
							<path d="M18.36 6.64a9 9 0 1 1-12.73 0"/>
							<line x1="12" y1="2" x2="12" y2="12"/>
						</svg>
					</button>
					<div class="iface-actions">
						<button
							class="icon-btn"
							title="Вверх"
							disabled={index === 0}
							onclick={() => moveUp(index)}
						>
							<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
								<polyline points="18 15 12 9 6 15"/>
							</svg>
						</button>
						<button
							class="icon-btn"
							title="Вниз"
							disabled={index === sorted.length - 1}
							onclick={() => moveDown(index)}
						>
							<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
								<polyline points="6 9 12 15 18 9"/>
							</svg>
						</button>
						<button
							class="icon-btn"
							title={iface.denied ? 'Разрешить использование' : 'Запретить использование'}
							onclick={() => iface.denied ? onpermit(iface.name, iface.order) : ondeny(iface.name)}
						>
							{#if iface.denied}
								<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
									<polyline points="20 6 9 17 4 12"/>
								</svg>
							{:else}
								<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
									<circle cx="12" cy="12" r="10"/>
									<line x1="4.93" y1="4.93" x2="19.07" y2="19.07"/>
								</svg>
							{/if}
						</button>
					</div>
				</div>
			{/each}
		</div>
	{/if}
</div>

{#if confirmToggle}
	<Modal open={true} title={confirmToggle.currentlyUp ? 'Выключение интерфейса' : 'Включение интерфейса'} size="sm" onclose={() => confirmToggle = null}>
		<div class="confirm-content">
			{#if confirmToggle.currentlyUp}
				<p class="confirm-warning">Это действие <strong>выключит</strong> интерфейс <strong>{confirmToggle.label}</strong> на роутере!</p>
				<p class="confirm-detail">Интерфейс перестанет работать для всех сервисов, не только для этой политики. Все подключения через этот интерфейс будут разорваны.</p>
			{:else}
				<p class="confirm-info">Это действие <strong>включит</strong> интерфейс <strong>{confirmToggle.label}</strong> на роутере.</p>
				<p class="confirm-detail">Интерфейс станет доступен для всех сервисов, не только для этой политики.</p>
			{/if}
		</div>
		{#snippet actions()}
			<button class="btn btn-ghost" onclick={() => confirmToggle = null}>Отмена</button>
			{#if confirmToggle?.currentlyUp}
				<button class="btn btn-danger" onclick={executeToggle}>Выключить</button>
			{:else}
				<button class="btn btn-primary" onclick={executeToggle}>Включить</button>
			{/if}
		{/snippet}
	</Modal>
{/if}

<style>
	.confirm-content {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.confirm-warning {
		color: var(--error);
		font-size: 0.9375rem;
		margin: 0;
	}

	.confirm-info {
		color: var(--text-primary);
		font-size: 0.9375rem;
		margin: 0;
	}

	.confirm-detail {
		color: var(--text-muted);
		font-size: 0.8125rem;
		margin: 0;
	}
	.iface-section {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.section-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
	}

	.section-header h4 {
		font-size: 0.8125rem;
		font-weight: 600;
		margin: 0;
		color: var(--text-primary);
	}

	.link-btn {
		background: none;
		border: none;
		color: var(--accent);
		cursor: pointer;
		font-size: 0.8125rem;
		padding: 0;
	}

	.link-btn:hover {
		text-decoration: underline;
	}

	.add-dropdown {
		display: flex;
		flex-direction: column;
		border: 1px solid var(--border);
		border-radius: 6px;
		overflow: hidden;
	}

	.dropdown-item {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 8px 12px;
		border: none;
		background: var(--bg-secondary);
		color: var(--text-primary);
		cursor: pointer;
		font-size: 0.8125rem;
		text-align: left;
	}

	.dropdown-item:hover {
		background: var(--bg-hover);
	}

	.dropdown-item + .dropdown-item {
		border-top: 1px solid var(--border);
	}

	.iface-down {
		font-size: 0.6875rem;
		color: var(--text-muted);
	}

	.empty-text {
		font-size: 0.8125rem;
		color: var(--text-muted);
		margin: 0;
	}

	.iface-list {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.iface-row {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 6px 10px;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--bg-secondary);
	}

	.iface-order {
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--text-muted);
		min-width: 18px;
		text-align: center;
	}

	.iface-label {
		flex: 1;
		font-size: 0.8125rem;
		font-weight: 500;
	}

	.iface-actions {
		display: flex;
		gap: 2px;
	}

	.icon-btn {
		display: flex;
		padding: 3px;
		background: none;
		border: none;
		color: var(--border-hover);
		cursor: pointer;
		border-radius: 4px;
		transition: color 0.15s;
	}

	.icon-btn:hover {
		color: var(--accent);
	}

	.icon-btn:disabled {
		opacity: 0.3;
		cursor: default;
	}

	.icon-btn.danger:hover {
		color: var(--error);
	}

	.icon-btn.active-toggle {
		color: var(--success);
	}

	.iface-row.denied {
		opacity: 0.5;
	}

	.denied-badge {
		font-size: 0.625rem;
		padding: 1px 6px;
		border-radius: 9999px;
		background: rgba(239, 68, 68, 0.15);
		color: var(--error);
		font-weight: 500;
		white-space: nowrap;
	}

	.led {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.led-green {
		background: var(--success);
		box-shadow: 0 0 4px var(--success);
	}

	.led-gray {
		background: var(--text-muted);
	}
</style>
