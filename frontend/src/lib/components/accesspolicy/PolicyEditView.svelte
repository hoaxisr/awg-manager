<script lang="ts">
	import type { AccessPolicy, PolicyDevice, PolicyGlobalInterface } from '$lib/types';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Toggle } from '$lib/components/ui';
	import { InterfaceList } from '$lib/components/accesspolicy';
	import { DeviceList } from '$lib/components/accesspolicy';

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

	async function saveDescription() {
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
			<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
				<line x1="19" y1="12" x2="5" y2="12"/>
				<polyline points="12 19 5 12 12 5"/>
			</svg>
			Назад к списку
		</button>

		<div class="field-group">
			<label class="field-label">Описание
				<input
					type="text"
					class="field-input"
					bind:value={description}
					onblur={saveDescription}
					maxlength={MAX_LEN}
					pattern="[a-zA-Z0-9_-]+"
				/>
				<span class="field-hint">Латинские буквы, цифры, дефисы, подчёркивания</span>
			</label>
		</div>

		<Toggle
			checked={policy.standalone}
			onchange={toggleStandalone}
			label="Standalone"
			hint="Политика действует самостоятельно, без привязки к глобальным правилам"
		/>

		<InterfaceList
			interfaces={localInterfaces}
			availableInterfaces={globalInterfaces}
			onpermit={handlePermit}
			ondeny={handleDeny}
			onreorder={handleReorder}
			onupdate={onupdate}
		/>

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
									<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
										<line x1="18" y1="6" x2="6" y2="18"/>
										<line x1="6" y1="6" x2="18" y2="18"/>
									</svg>
								</button>
							</div>
						{/each}
					</div>
				{/if}
			</div>
		</div>
	</div>

	<div class="right-panel">
		<DeviceList
			{devices}
			currentPolicy={policy.name}
			onassign={assignDevice}
		/>
	</div>
</div>

<style>
	.edit-layout {
		display: grid;
		grid-template-columns: 1.3fr 1fr;
		min-height: 0;
	}

	@media (max-width: 768px) {
		.edit-layout {
			grid-template-columns: 1fr;
		}

		.left-panel {
			border-right: none !important;
			border-bottom: 1px solid var(--border);
		}
	}

	.left-panel {
		display: flex;
		flex-direction: column;
		gap: 16px;
		padding: 16px;
		border-right: 1px solid var(--border);
	}

	.right-panel {
		padding: 16px;
		background: var(--bg-primary);
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
