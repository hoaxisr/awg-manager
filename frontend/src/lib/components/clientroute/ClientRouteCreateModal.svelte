<script lang="ts">
	import { Modal, Button, Dropdown, type DropdownOption } from '$lib/components/ui';
	import type { ClientRoute, PolicyDevice, RoutingTunnel } from '$lib/types';

	interface Props {
		open: boolean;
		editing: ClientRoute | null;
		devices: PolicyDevice[];
		tunnels: RoutingTunnel[];
		existingIPs: string[];
		saving: boolean;
		onsave: (data: Partial<ClientRoute>) => void;
		onclose: () => void;
	}

	let {
		open = $bindable(false),
		editing,
		devices,
		tunnels,
		existingIPs,
		saving,
		onsave,
		onclose
	}: Props = $props();

	// ClientRoute's OnTunnelStart path calls SetupClientRouteTable with the
	// target's kernel interface — designed for WG-style tunnels. Routing a
	// device directly to a WAN interface would need a different backend path
	// (ip rule pointing at a WAN-gateway table), which isn't implemented yet.
	// Until it is, exclude WAN targets here so users can't save rules the
	// backend can't apply.
	let availableTunnels = $derived(tunnels.filter(t => t.type !== 'wan' && t.available));
	let tunnelOpts = $derived<DropdownOption[]>(availableTunnels.map((t) => ({
		value: t.id,
		label: t.name,
	})));

	let selectedDevice = $state<{ ip: string; name: string } | null>(null);
	let searchText = $state('');
	let selectedTunnel = $state('');
	let selectedFallback = $state<'drop' | 'bypass'>('drop');

	let filteredDevices = $derived(
		devices.filter((d) => {
			const q = searchText.toLowerCase();
			return d.name.toLowerCase().includes(q) || d.ip.toLowerCase().includes(q);
		})
	);

	let isManualIP = $derived(() => {
		if (!searchText.trim()) return false;
		const parts = searchText.trim().split('.');
		if (parts.length !== 4) return false;
		return parts.every(p => { const n = Number(p); return Number.isInteger(n) && n >= 0 && n <= 255 && p !== ''; });
	});

	let showManualOption = $derived(
		!editing && filteredDevices.length === 0 && isManualIP() && !existingIPs.includes(searchText.trim())
	);

	let canSave = $derived(selectedDevice !== null && selectedTunnel !== '');
	let attempted = $state(false);
	let wasOpen = $state(false);

	let deviceError = $derived(attempted && selectedDevice === null);

	let title = $derived(editing ? 'Редактирование правила' : 'VPN для устройства');

	$effect(() => {
		if (!open) {
			wasOpen = false;
			return;
		}
		if (wasOpen) return; // already initialised — user may be editing
		wasOpen = true;
		attempted = false;
		if (editing) {
			selectedDevice = { ip: editing.clientIp, name: editing.clientHostname };
			selectedTunnel = editing.tunnelId;
			selectedFallback = editing.fallback;
		} else {
			selectedDevice = null;
			selectedTunnel = availableTunnels[0]?.id ?? '';
			selectedFallback = 'drop';
		}
		searchText = '';
	});

	function handleSave() {
		attempted = true;
		if (!canSave) {
			// TODO Phase 1: restore shake animation feedback on invalid submit
			return;
		}
		onsave({
			clientIp: selectedDevice!.ip,
			clientHostname: selectedDevice!.name,
			tunnelId: selectedTunnel,
			fallback: selectedFallback,
			enabled: editing?.enabled ?? true
		});
	}

	function isDeviceDisabled(device: PolicyDevice): boolean {
		return existingIPs.includes(device.ip);
	}

	function selectDevice(device: PolicyDevice) {
		if (editing || isDeviceDisabled(device)) return;
		selectedDevice = { ip: device.ip, name: device.name };
	}
</script>

<Modal {open} {title} size="md" {onclose}>
	<div class="form-sections">
		<!-- Device list -->
		<div class="section" class:field-error={deviceError}>
			<span class="section-label">Устройство</span>
			<input
				type="text"
				class="search-input"
				placeholder="Поиск по имени или IP..."
				bind:value={searchText}
				disabled={!!editing}
			/>
			<div class="device-list" class:disabled={!!editing}>
				{#each filteredDevices as device (device.mac)}
					{@const disabled = isDeviceDisabled(device)}
					{@const selected = selectedDevice?.ip === device.ip}
					<button
						type="button"
						class="device-row"
						class:selected
						class:disabled
						onclick={() => selectDevice(device)}
						disabled={disabled || !!editing}
					>
						<span class="device-name">{device.name}</span>
						<span class="device-status" class:online={device.active}></span>
						<span class="device-ip">{device.ip}</span>
					</button>
				{:else}
					<div class="empty-list">Устройства не найдены</div>
				{/each}
				{#if showManualOption}
					<button
						type="button"
						class="manual-ip-btn"
						class:selected={selectedDevice?.ip === searchText.trim()}
						onclick={() => { selectedDevice = { ip: searchText.trim(), name: '' }; }}
					>
						Использовать: {searchText.trim()}
					</button>
				{/if}
			</div>
			<div class="error-text" class:visible={deviceError}>Выберите устройство</div>
		</div>

		<!-- Tunnel dropdown -->
		<div class="section">
			<Dropdown
				id="tunnel-select"
				label="Туннель"
				bind:value={selectedTunnel}
				options={tunnelOpts}
				fullWidth
			/>
		</div>

		<!-- Fallback selector -->
		<div class="section">
			<span class="section-label">Если туннель недоступен</span>
			<div class="fallback-cards">
				<button
					type="button"
					class="fallback-card"
					class:active={selectedFallback === 'drop'}
					onclick={() => (selectedFallback = 'drop')}
				>
					<span class="fallback-title">Блокировать</span>
					<span class="fallback-subtitle">Kill Switch</span>
				</button>
				<button
					type="button"
					class="fallback-card"
					class:active={selectedFallback === 'bypass'}
					onclick={() => (selectedFallback = 'bypass')}
				>
					<span class="fallback-title">Напрямую</span>
					<span class="fallback-subtitle">Bypass VPN</span>
				</button>
			</div>
		</div>

		<!-- Warning -->
		{#if !editing}
			<div class="warning-box">
				&#9888; Для гарантированной работы назначьте устройству статический IP-адрес в настройках роутера
			</div>
		{/if}
	</div>

	{#snippet actions()}
		<Button variant="ghost" onclick={onclose} disabled={saving}>Отмена</Button>
		<!-- TODO Phase 1: shake animation on save when invalid (was class:shake={shaking}) -->
		<Button variant="primary" onclick={handleSave} loading={saving}>
			{editing ? 'Сохранить' : 'Создать'}
		</Button>
	{/snippet}
</Modal>

<style>
	.form-sections {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.section {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.section-label {
		font-size: 0.875rem;
		font-weight: 500;
		color: var(--color-text-primary);
	}

	.search-input {
		width: 100%;
		padding: 8px 12px;
		border: 1px solid var(--color-border);
		border-radius: 6px;
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		font-size: 0.875rem;
		outline: none;
		transition: border-color 0.15s;
	}

	.search-input:focus {
		border-color: var(--color-accent);
	}

	.search-input:disabled {
		opacity: 0.6;
	}

	.device-list {
		max-height: 150px;
		overflow-y: auto;
		border: 1px solid var(--color-border);
		border-radius: 6px;
		background: var(--color-bg-primary);
	}

	.device-list.disabled {
		opacity: 0.6;
		pointer-events: none;
	}

	.device-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		width: 100%;
		padding: 8px 12px;
		border: none;
		background: transparent;
		color: var(--color-text-primary);
		font-size: 0.875rem;
		cursor: pointer;
		text-align: left;
		transition: background 0.15s;
	}

	.device-row:hover:not(.disabled) {
		background: var(--color-bg-hover);
	}

	.device-row.selected {
		background: color-mix(in srgb, var(--color-accent) 15%, transparent);
	}

	.device-row.disabled {
		opacity: 0.4;
		cursor: not-allowed;
	}

	.device-row + .device-row {
		border-top: 1px solid var(--color-border);
	}

	.device-name {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.device-status {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--color-text-muted);
		flex-shrink: 0;
	}

	.device-status.online {
		background: var(--success, #22c55e);
	}

	.device-ip {
		color: var(--color-text-muted);
		font-size: 0.8rem;
		flex-shrink: 0;
	}

	.empty-list {
		padding: 1rem;
		text-align: center;
		color: var(--color-text-muted);
		font-size: 0.875rem;
	}

	.manual-ip-btn {
		display: block;
		width: 100%;
		padding: 8px 12px;
		border: none;
		border-top: 1px solid var(--color-border);
		background: transparent;
		color: var(--color-accent);
		font-size: 0.875rem;
		cursor: pointer;
		text-align: left;
		transition: background 0.15s;
	}

	.manual-ip-btn:hover {
		background: var(--color-bg-hover);
	}

	.manual-ip-btn.selected {
		background: color-mix(in srgb, var(--color-accent) 15%, transparent);
	}

	.fallback-cards {
		display: flex;
		gap: 0.75rem;
	}

	.fallback-card {
		flex: 1;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.25rem;
		padding: 0.75rem;
		border: 2px solid var(--color-border);
		border-radius: 8px;
		background: var(--color-bg-primary);
		cursor: pointer;
		transition: border-color 0.15s;
	}

	.fallback-card:hover {
		border-color: var(--color-text-muted);
	}

	.fallback-card.active {
		border-color: var(--color-accent);
	}

	.fallback-title {
		font-size: 0.875rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.fallback-subtitle {
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}

	.field-error .device-list {
		border-color: var(--error, #ef4444);
		box-shadow: 0 0 0 2px rgba(239, 68, 68, 0.15);
	}

	.warning-box {
		padding: 0.75rem 1rem;
		background: rgba(234, 179, 8, 0.1);
		border: 1px solid var(--warning, #eab308);
		border-radius: 6px;
		color: var(--warning, #eab308);
		font-size: 0.8125rem;
		line-height: 1.4;
	}
</style>
