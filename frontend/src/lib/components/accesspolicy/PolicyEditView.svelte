<script lang="ts">
	import type { AccessPolicy, PolicyDevice, PolicyGlobalInterface } from '$lib/types';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Toggle, Badge } from '$lib/components/ui';
	import { ArrowLeft, X } from 'lucide-svelte';
	import { InterfaceList } from '$lib/components/accesspolicy';
	import { DeviceList } from '$lib/components/accesspolicy';
	import { isHydraRouteAccessPolicy } from '$lib/utils/accessPolicy';

	interface Props {
		policy: AccessPolicy;
		devices: PolicyDevice[];
		globalInterfaces: PolicyGlobalInterface[];
		onback: () => void;
		onupdate: () => Promise<void>;
		ondeviceassigned: (mac: string, policyName: string) => void;
		ondeviceunassigned: (mac: string, fromPolicy: string) => void;
	}

	let { policy, devices, globalInterfaces, onback, onupdate, ondeviceassigned, ondeviceunassigned }: Props = $props();

	let isHrPolicy = $derived(isHydraRouteAccessPolicy(policy));

	let description = $state('');
	let localInterfaces = $state<import('$lib/types').AccessPolicyInterface[]>([]);
	let dragOver = $state(false);
	const VALID_PATTERN = /^[a-zA-Z0-9_-]*$/;
	const MAX_LEN = 256;

	$effect(() => {
		description = policy.description;
		localInterfaces = policy.interfaces ?? [];
	});

	let assignedDevices = $derived(devices.filter((d) => d.policy === policy.name));
	let descriptionValid = $derived(description.trim().length > 0 && description.trim().length <= MAX_LEN && VALID_PATTERN.test(description.trim()));
	const standaloneHint = $derived(
		policy.standalone
			? 'Политика действует самостоятельно, без привязки к глобальным правилам. Статические маршруты из настроек роутера не копируются (маршруты из вкладки "IP-адреса" работать не будут).'
			: 'Если standalone отключено - в политике действуют глобальные правила, статические маршруты из настроек роутера копируются.',
	);

	async function saveDescription() {
		if (isHrPolicy) return;
		if (description.trim() === policy.description) return;
		if (!descriptionValid) {
			notifications.error('Описание: только латинские буквы, цифры, дефисы и подчёркивания');
			description = policy.description;
			return;
		}
		try {
			await api.setAccessPolicyDescription(policy.name, description.trim());
			await onupdate();
		} catch (e: any) {
			notifications.error(`Ошибка: ${e.message}`);
		}
	}

	async function toggleStandalone(checked: boolean) {
		if (isHrPolicy) return;
		try {
			await api.setAccessPolicyStandalone(policy.name, checked);
			await onupdate();
		} catch (e: any) {
			notifications.error(`Ошибка: ${e.message}`);
		}
	}

	async function handlePermit(iface: string, order: number) {
		try {
			await api.permitPolicyInterface(policy.name, iface, order);
			await onupdate();
		} catch (e: any) {
			notifications.error(`Ошибка: ${e.message}`);
		}
	}

	async function handleDeny(iface: string) {
		try {
			await api.denyPolicyInterface(policy.name, iface);
			await onupdate();
		} catch (e: any) {
			notifications.error(`Ошибка: ${e.message}`);
		}
	}

	async function handleReorder(iface: string, newOrder: number) {
		try {
			const policyName = policy.name;
			await api.permitPolicyInterface(policyName, iface, newOrder);
			await onupdate();
		} catch (e: any) {
			notifications.error(`Ошибка: ${e.message}`);
		}
	}

	async function assignDevice(mac: string) {
		if (isHrPolicy) return;
		try {
			await api.assignDeviceToPolicy(mac, policy.name);
			ondeviceassigned(mac, policy.name);
		} catch (e: any) {
			dragOver = false;
			notifications.error(`Ошибка: ${e.message}`);
		}
	}

	async function unassignDevice(mac: string) {
		try {
			await api.unassignDeviceFromPolicy(mac);
			ondeviceunassigned(mac, policy.name);
		} catch (e: any) {
			notifications.error(`Ошибка: ${e.message}`);
		}
	}

	function handleDrop(e: DragEvent) {
		e.preventDefault();
		dragOver = false;
		const mac = e.dataTransfer?.getData('text/plain');
		if (mac) {
			assignDevice(mac);
		}
	}

	function handleDragOver(e: DragEvent) {
		e.preventDefault();
		dragOver = true;
	}

	function handleDragLeave() {
		dragOver = false;
	}
</script>

<div class="edit-layout">
	<div class="left-panel">
		<button class="back-btn" onclick={onback}>
			<ArrowLeft size={15} />
			Назад к списку
		</button>

		{#if !isHrPolicy}
			<div class="field-group">
				<label class="field-label">Описание
					<input
						type="text"
						class="field-input"
						bind:value={description}
						onblur={saveDescription}
						maxlength={MAX_LEN}
					/>
					<span class="field-hint">Латинские буквы, цифры, дефисы, подчёркивания</span>
				</label>
			</div>

			<Toggle
				checked={policy.standalone}
				onchange={toggleStandalone}
				spinner="after"
				label="Standalone"
				hint={standaloneHint}
			/>
		{/if}

		<InterfaceList
			interfaces={localInterfaces}
			availableInterfaces={globalInterfaces}
			addPickerVariant="panel"
			onpermit={handlePermit}
			ondeny={handleDeny}
			onreorder={handleReorder}
			onupdate={onupdate}
		/>

		{#if !isHrPolicy}
		<div class="assigned-section">
			<h4 class="section-title">Устройства в политике</h4>

			<!-- svelte-ignore a11y_no_static_element_interactions -->
			<div
				class="drop-zone"
				class:drag-active={dragOver}
				ondrop={handleDrop}
				ondragover={handleDragOver}
				ondragleave={handleDragLeave}
			>
				{#if assignedDevices.length === 0}
					<p class="drop-placeholder">перетащите устройство сюда</p>
				{:else}
					<div class="assigned-list">
						{#each assignedDevices as device}
							{@const isActive = device.active && device.link === 'up'}
							<div class="assigned-row">
								<span class="led" class:led-green={isActive} class:led-gray={!isActive}></span>
								<div class="device-info">
									<span class="device-name">{device.name || device.hostname || device.mac}</span>
									{#if device.ip}
										<span class="device-ip">{device.ip}</span>
									{/if}
								</div>
								<button
									class="remove-btn"
									title="Убрать из политики"
									onclick={() => unassignDevice(device.mac)}
								>
									<X size={15} />
								</button>
							</div>
						{/each}
					</div>
				{/if}
			</div>
		</div>
		{/if}
	</div>

	<div class="right-panel" class:right-panel-hr={isHrPolicy}>
		{#if isHrPolicy}
			<div class="hr-side">
				<div class="hr-policy-banner">
					<Badge variant="warning" uppercase size="xs" pill>HydraRoute</Badge>
					<p>
						Это политика HydraRoute Neo. Добавлять в неё устройства не требуется — маршрутизация
						HydraRoute распространяется только на политику по умолчанию. Интерфейсы настраиваются
						тем же способом, что и на вкладке HR Neo.
					</p>
				</div>
				{#if assignedDevices.length > 0}
					<div class="hr-assigned-section">
						<h4 class="section-title">Привязанные устройства</h4>
						<div class="assigned-list">
							{#each assignedDevices as device}
								{@const isActive = device.active && device.link === 'up'}
								<div class="assigned-row">
									<span class="led" class:led-green={isActive} class:led-gray={!isActive}></span>
									<div class="device-info">
										<span class="device-name">{device.name || device.hostname || device.mac}</span>
										{#if device.ip}
											<span class="device-ip">{device.ip}</span>
										{/if}
									</div>
									<button
										class="remove-btn"
										title="Убрать из политики"
										onclick={() => unassignDevice(device.mac)}
									>
										<X size={15} />
									</button>
								</div>
							{/each}
						</div>
					</div>
				{/if}
			</div>
		{:else}
			<DeviceList
				{devices}
				currentPolicy={policy.name}
				onassign={assignDevice}
			/>
		{/if}
	</div>
</div>

<style>
	.edit-layout {
		display: grid;
		grid-template-columns: 1.3fr 1fr;
		flex: 1;
		min-height: 0;
		height: 100%;
		overflow: hidden;
	}

	@media (max-width: 768px) {
		.edit-layout {
			grid-template-columns: 1fr;
			grid-template-rows: auto;
			height: auto;
			overflow: visible;
		}

		.left-panel {
			border-right: none !important;
			border-bottom: 1px solid var(--border);
			overflow: visible;
			min-height: auto;
		}

		.right-panel {
			overflow: visible;
			min-height: auto;
		}

		.right-panel-hr {
			overflow-y: visible;
		}
	}

	.left-panel {
		display: flex;
		flex-direction: column;
		gap: 16px;
		padding: 16px;
		border-right: 1px solid var(--border);
		min-height: 0;
		overflow-y: auto;
	}

	.right-panel {
		display: flex;
		flex-direction: column;
		min-height: 0;
		overflow: hidden;
		padding: 16px;
		background: var(--bg-primary);
	}

	.right-panel-hr {
		overflow-y: auto;
	}

	.hr-side {
		display: flex;
		flex-direction: column;
		gap: 16px;
		width: 100%;
	}

	.hr-assigned-section {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.back-btn {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		background: none;
		border: none;
		color: var(--accent);
		cursor: pointer;
		font-size: 0.8125rem;
		padding: 0;
	}

	.back-btn:hover {
		text-decoration: underline;
	}

	.field-group {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.field-hint {
		font-size: 0.75rem;
		color: var(--text-secondary);
	}

	.field-label {
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--text-muted);
	}

	.field-input {
		width: 100%;
		padding: 8px 12px;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--bg-primary);
		color: var(--text-primary);
		font-size: 0.875rem;
		outline: none;
		transition: border-color 0.15s;
	}

	.field-input:focus {
		border-color: var(--accent);
	}

	.hr-policy-banner {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 12px;
		width: 100%;
		padding: 16px;
		border-radius: 8px;
		border: 1px solid rgba(245, 158, 11, 0.35);
		background: rgba(245, 158, 11, 0.08);
		box-sizing: border-box;
	}

	.hr-policy-banner p {
		margin: 0;
		font-size: 0.8125rem;
		line-height: 1.45;
		color: var(--text-secondary);
	}

	.assigned-section {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.section-title {
		font-size: 0.8125rem;
		font-weight: 600;
		margin: 0;
		color: var(--text-primary);
	}

	.drop-zone {
		min-height: 80px;
		border: 2px dashed var(--border);
		border-radius: 8px;
		padding: 8px;
		transition: border-color 0.15s, background 0.15s;
	}

	.drop-zone.drag-active {
		border-color: var(--accent);
		background: rgba(59, 130, 246, 0.05);
	}

	.drop-placeholder {
		color: var(--text-muted);
		font-size: 0.8125rem;
		text-align: center;
		margin: 20px 0;
	}

	.assigned-list {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.assigned-row {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 6px 10px;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--bg-secondary);
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

	.remove-btn {
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

	.remove-btn:hover {
		color: var(--error);
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
</style>
