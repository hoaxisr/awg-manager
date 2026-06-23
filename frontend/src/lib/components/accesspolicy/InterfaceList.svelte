<script lang="ts">
	import type { AccessPolicyInterface, PolicyGlobalInterface } from '$lib/types';
	import { ConfirmModal, Badge, Button } from '$lib/components/ui';
	import { Power, ChevronUp, ChevronDown, Check, Ban } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import {
		filterPolicyGlobalInterfaces,
		groupPolicyGlobalInterfaces,
		policyInterfaceDisplayLabel,
	} from '$lib/utils/routingTunnelOptions';

	interface Props {
		interfaces: AccessPolicyInterface[];
		availableInterfaces: PolicyGlobalInterface[];
		/** scroll: toggle + capped scroll. panel: toggle + full list без скролла (HR Neo, политики доступа). */
		addPickerVariant?: 'scroll' | 'panel';
		onpermit: (iface: string, order: number) => void;
		ondeny: (iface: string) => void;
		onreorder: (iface: string, newOrder: number) => void;
		onupdate: () => void;
	}

	let {
		interfaces: rawInterfaces,
		availableInterfaces,
		addPickerVariant = 'scroll',
		onpermit,
		ondeny,
		onreorder,
		onupdate,
	}: Props = $props();

	const isPanelPicker = $derived(addPickerVariant === 'panel');

	let interfaces = $derived(rawInterfaces ?? []);
	let showAdd = $state(false);

	let sorted = $derived([...interfaces].sort((a, b) => a.order - b.order));

	let catalog = $derived(filterPolicyGlobalInterfaces(availableInterfaces));

	let unassigned = $derived(
		catalog.filter((gi) => !interfaces.some((i) => i.name === gi.name)),
	);

	let unassignedGroups = $derived(groupPolicyGlobalInterfaces(unassigned));

	function handleAdd(iface: string) {
		onpermit(iface, interfaces.length);
		showAdd = false;
	}

	function getLabel(name: string): string {
		const gi = catalog.find((g) => g.name === name);
		return gi ? policyInterfaceDisplayLabel(gi) : name;
	}

	function isUp(name: string): boolean {
		return catalog.find((gi) => gi.name === name)?.up ?? false;
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

<div class="iface-section" class:iface-section--panel={isPanelPicker}>
	<div class="section-header">
		<h4>Интерфейсы (приоритет)</h4>
		{#if unassigned.length > 0}
			<Button variant="secondary" size="sm" onclick={() => (showAdd = !showAdd)}>
				{showAdd ? 'Отмена' : 'Добавить'}
			</Button>
		{/if}
	</div>

	{#if showAdd}
		<div class="add-dropdown" class:add-dropdown--panel={isPanelPicker}>
			{#each unassignedGroups as { group, items }}
				<div class="group-label" role="presentation">{group}</div>
				{#each items as gi (gi.name)}
					<button class="dropdown-item in-group" onclick={() => handleAdd(gi.name)}>
						<span class="iface-name">{policyInterfaceDisplayLabel(gi)}</span>
						{#if !gi.up}
							<span class="iface-down">down</span>
						{/if}
					</button>
				{/each}
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
						<Badge variant="error" size="xs">запрещён</Badge>
					{/if}
					<button
						class="icon-btn"
						class:active-toggle={isUp(iface.name)}
						title={isUp(iface.name) ? 'Выключить интерфейс' : 'Включить интерфейс'}
						disabled={toggling === iface.name}
						onclick={() => requestToggle(iface.name)}
					>
						<Power size={14} />
					</button>
					<div class="iface-actions">
						<button
							class="icon-btn"
							title="Вверх"
							disabled={index === 0}
							onclick={() => moveUp(index)}
						>
							<ChevronUp size={15} />
						</button>
						<button
							class="icon-btn"
							title="Вниз"
							disabled={index === sorted.length - 1}
							onclick={() => moveDown(index)}
						>
							<ChevronDown size={15} />
						</button>
						<button
							class="icon-btn"
							title={iface.denied ? 'Разрешить использование' : 'Запретить использование'}
							onclick={() => iface.denied ? onpermit(iface.name, interfaces.filter(i => !i.denied).length) : ondeny(iface.name)}
						>
							{#if iface.denied}
								<Check size={15} />
							{:else}
								<Ban size={15} />
							{/if}
						</button>
					</div>
				</div>
			{/each}
		</div>
	{/if}
</div>

{#if confirmToggle}
	<ConfirmModal
		open={true}
		title={confirmToggle.currentlyUp ? 'Выключение интерфейса' : 'Включение интерфейса'}
		message={confirmToggle.currentlyUp
			? `Это действие выключит интерфейс «${confirmToggle.label}» на роутере.`
			: `Это действие включит интерфейс «${confirmToggle.label}» на роутере.`}
		secondary={confirmToggle.currentlyUp
			? 'Интерфейс перестанет работать для всех сервисов, не только для этой политики. Все подключения через этот интерфейс будут разорваны.'
			: 'Интерфейс станет доступен для всех сервисов, не только для этой политики.'}
		variant={confirmToggle.currentlyUp ? 'danger' : 'primary'}
		confirmLabel={confirmToggle.currentlyUp ? 'Выключить' : 'Включить'}
		onConfirm={executeToggle}
		onClose={() => confirmToggle = null}
	/>
{/if}

<style>
	.iface-section {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.section-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		width: 100%;
		gap: 12px;
	}

	.section-header h4 {
		font-size: 0.8125rem;
		font-weight: 600;
		margin: 0;
		color: var(--text-primary);
	}

	@media (max-width: 640px) {
		.section-header {
			display: grid;
			grid-template-columns: repeat(2, minmax(0, 1fr));
			align-items: center;
		}

		.section-header h4 {
			min-width: 0;
			text-align: left;
		}
	}

	.add-dropdown {
		display: flex;
		flex-direction: column;
		border: 1px solid var(--border);
		border-radius: 8px;
		overflow: hidden;
		max-height: 280px;
		overflow-y: auto;
	}

	.add-dropdown--panel {
		max-height: none;
		overflow: visible;
		overflow-y: visible;
		margin-bottom: 4px;
	}

	.add-dropdown--panel .dropdown-item:first-of-type,
	.add-dropdown--panel .group-label:first-child + .dropdown-item {
		border-top: none;
	}

	.add-dropdown--panel .group-label:first-child {
		border-radius: 7px 7px 0 0;
	}

	.add-dropdown--panel .dropdown-item:last-child {
		border-radius: 0 0 7px 7px;
	}

	.iface-section--panel {
		gap: 10px;
	}

	.iface-section--panel .iface-list {
		gap: 6px;
	}

	.group-label {
		padding: 0.35rem 0.75rem 0.2rem;
		font-size: 0.65rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--text-muted, var(--color-text-muted));
		background: var(--bg-primary, var(--color-bg-primary));
		position: sticky;
		top: 0;
		z-index: 1;
	}

	.add-dropdown--panel .group-label {
		padding: 0.45rem 0.875rem 0.3rem;
		position: static;
	}

	.add-dropdown--panel .dropdown-item {
		padding: 10px 14px;
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

	.dropdown-item.in-group + .dropdown-item.in-group,
	.group-label + .dropdown-item {
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

	.iface-section--panel .iface-row {
		padding: 8px 12px;
		gap: 10px;
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
		min-width: 0;
		font-size: 0.8125rem;
		font-weight: 500;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
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

	.icon-btn.active-toggle {
		color: var(--success);
	}

	.iface-row.denied {
		opacity: 0.5;
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
