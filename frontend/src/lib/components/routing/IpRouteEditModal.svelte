<script lang="ts">
	import type { StaticRouteList, RoutingTunnel } from '$lib/types';
	import { Modal } from '$lib/components/ui';

	interface Props {
		open: boolean;
		route: StaticRouteList | null;
		tunnels: RoutingTunnel[];
		saving: boolean;
		onsave: (data: { name: string; tunnelID: string; subnets: string[]; fallback: '' | 'reject' }) => void;
		onclose: () => void;
	}

	let { open, route, tunnels: rawTunnels, saving, onsave, onclose }: Props = $props();
	let tunnels = $derived((rawTunnels ?? []).filter(t => t.available || t.type === 'wan'));

	// Form state
	let name = $state('');
	let tunnelID = $state('');
	let fallback = $state<'' | 'reject'>('');
	let subnetsText = $state('');
	let isInitialized = $state(false);
	let attempted = $state(false);
	let shaking = $state(false);

	// Reset form when modal opens (only once per open, not on every poll tick)
	$effect(() => {
		if (open) {
			if (!isInitialized) {
				attempted = false;
				if (route) {
					name = route.name;
					tunnelID = route.tunnelID;
					fallback = route.fallback || '';
					subnetsText = (route.subnets ?? []).join('\n');
				} else {
					name = '';
					tunnelID = tunnels.length > 0 ? tunnels[0].id : '';
					fallback = '';
					subnetsText = '';
				}
				isInitialized = true;
			}
		} else {
			isInitialized = false;
		}
	});

	// Computed
	let isEdit = $derived(route !== null);
	let title = $derived(isEdit ? `Редактирование: ${route?.name ?? ''}` : 'Новый IP-маршрут');

	let parsedSubnets = $derived(
		subnetsText
			.split('\n')
			.map(s => s.trim())
			.filter(s => s !== '')
	);

	let canSave = $derived(name.trim() !== '' && tunnelID !== '' && parsedSubnets.length > 0);

	let nameError = $derived(attempted && name.trim() === '');
	let tunnelError = $derived(attempted && tunnelID === '');
	let subnetError = $derived(attempted && parsedSubnets.length === 0);

	let userTunnels = $derived(tunnels.filter(t => t.type === 'managed'));
	let systemTunnels = $derived(tunnels.filter(t => t.type === 'system'));
	let wanInterfaces = $derived(tunnels.filter(t => t.type === 'wan'));

	// OS4 kernel tunnels (awgmX) don't support kill switch — interface destruction
	// removes routes, so "reject" fallback has no effect.
	let isOS4Kernel = $derived(tunnelID.startsWith('awgm'));

	// Reset fallback to bypass when switching to OS4 kernel tunnel
	$effect(() => {
		if (isOS4Kernel && fallback === 'reject') {
			fallback = '';
		}
	});

	// .bat file import
	let batInput: HTMLInputElement | undefined = $state(undefined);

	function handleBatImport() {
		batInput?.click();
	}

	async function handleBatFile(e: Event) {
		const input = e.target as HTMLInputElement;
		const file = input.files?.[0];
		if (!file) return;
		try {
			const text = await file.text();
			const lines = text.split('\n');
			const subnets: string[] = [];
			for (const line of lines) {
				const trimmed = line.trim();
				// Match "route add X.X.X.X mask Y.Y.Y.Y GW [metric N] [!comment]"
				const routeMatch = trimmed.match(/route\s+add\s+(\d+\.\d+\.\d+\.\d+)\s+mask\s+(\d+\.\d+\.\d+\.\d+)\s+\S+(?:\s+metric\s+\d+)?\s*(!.+)?/i);
				if (routeMatch) {
					const cidr = maskToCidr(routeMatch[1], routeMatch[2]);
					if (cidr) {
						const comment = routeMatch[3] ? routeMatch[3].substring(1).trim() : '';
						subnets.push(comment ? `${cidr} !${comment}` : cidr);
					}
					continue;
				}
				// Also accept "CIDR [!comment]" lines
				const cidrMatch = trimmed.match(/^(\d+\.\d+\.\d+\.\d+\/\d+)(?:\s+(!.+))?$/);
				if (cidrMatch) {
					const comment = cidrMatch[2] ? cidrMatch[2].substring(1).trim() : '';
					subnets.push(comment ? `${cidrMatch[1]} !${comment}` : cidrMatch[1]);
				}
			}
			if (subnets.length > 0) {
				const existing = subnetsText.trim();
				subnetsText = existing ? existing + '\n' + subnets.join('\n') : subnets.join('\n');
			}
		} catch {
			// silently ignore read errors
		}
		input.value = '';
	}

	function maskToCidr(ip: string, mask: string): string | null {
		const parts = mask.split('.').map(Number);
		let bits = 0;
		for (const p of parts) {
			let v = p;
			while (v > 0) {
				bits += v & 1;
				v >>= 1;
			}
		}
		if (bits === 0 || bits > 32) return null;
		return `${ip}/${bits}`;
	}

	function handleSave() {
		attempted = true;
		if (!canSave) {
			shaking = true;
			setTimeout(() => shaking = false, 400);
			return;
		}
		onsave({
			name: name.trim(),
			tunnelID,
			subnets: parsedSubnets,
			fallback,
		});
	}
</script>

<Modal {open} {title} size="lg" onclose={onclose}>
	<!-- Name -->
	<div class="form-group" class:field-error={nameError}>
		<!-- svelte-ignore a11y_label_has_associated_control -->
		<label class="form-label">Название</label>
		<input
			class="form-input"
			type="text"
			placeholder="Заблокированные подсети"
			value={name}
			oninput={(e) => { name = (e.target as HTMLInputElement).value; }}
		/>
		<div class="error-text" class:visible={nameError}>Введите название</div>
	</div>

	<!-- Tunnel -->
	<div class="form-group" class:field-error={tunnelError}>
		<!-- svelte-ignore a11y_label_has_associated_control -->
		<label class="form-label">Туннель</label>
		<select
			class="form-select"
			value={tunnelID}
			onchange={(e) => { tunnelID = (e.target as HTMLSelectElement).value; }}
		>
			{#if userTunnels.length > 0}
				<optgroup label="Пользовательские">
					{#each userTunnels as tunnel}
						<option value={tunnel.id}>{tunnel.name}</option>
					{/each}
				</optgroup>
			{/if}
			{#if systemTunnels.length > 0}
				<optgroup label="Системные">
					{#each systemTunnels as tunnel}
						<option value={tunnel.id}>{tunnel.name}</option>
					{/each}
				</optgroup>
			{/if}
			{#if wanInterfaces.length > 0}
				<optgroup label="WAN">
					{#each wanInterfaces as tunnel}
						<option value={tunnel.id}>{tunnel.name}</option>
					{/each}
				</optgroup>
			{/if}
		</select>
		<div class="error-text" class:visible={tunnelError}>Выберите туннель</div>
	</div>

	<!-- Fallback -->
	<div class="form-group">
		<!-- svelte-ignore a11y_label_has_associated_control -->
		<label class="form-label">При недоступности интерфейса</label>
		<select
			class="form-select"
			value={fallback}
			onchange={(e) => { fallback = (e.target as HTMLSelectElement).value as '' | 'reject'; }}
		>
			<option value="">Bypass — трафик пойдёт обычным маршрутом</option>
			{#if !isOS4Kernel}
				<option value="reject">Kill Switch — трафик будет заблокирован</option>
			{/if}
		</select>
	</div>

	<!-- Subnets -->
	<div class="form-section" class:field-error={subnetError}>
		<div class="section-header">
			<div class="section-title">Подсети (по одной на строку, CIDR)</div>
			<button class="btn-bat-import" onclick={handleBatImport}>
				<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
					<path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4"/>
					<polyline points="17 8 12 3 7 8"/>
					<line x1="12" y1="3" x2="12" y2="15"/>
				</svg>
				Из .bat файла
			</button>
			<input
				bind:this={batInput}
				type="file"
				accept=".bat,.txt"
				onchange={handleBatFile}
				class="hidden-input"
			/>
		</div>
		<textarea
			class="form-textarea"
			placeholder="10.0.0.0/8&#10;192.168.1.0/24&#10;172.16.0.0/12"
			value={subnetsText}
			oninput={(e) => { subnetsText = (e.target as HTMLTextAreaElement).value; }}
			rows="8"
		></textarea>
		{#if parsedSubnets.length > 0}
			<span class="subnet-count">{parsedSubnets.length} подсетей</span>
		{/if}
		<div class="error-text" class:visible={subnetError}>Добавьте хотя бы одну подсеть</div>
	</div>

	{#snippet actions()}
		<button class="btn btn-secondary" onclick={onclose}>Отмена</button>
		<button class="btn btn-primary" class:shake={shaking} onclick={handleSave} disabled={saving}>
			{saving ? 'Сохранение...' : 'Сохранить'}
		</button>
	{/snippet}
</Modal>

<style>
	.form-group {
		margin-bottom: 1rem;
	}

	.form-label {
		display: block;
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--text-primary);
		margin-bottom: 0.375rem;
	}

	.form-input,
	.form-select {
		width: 100%;
		padding: 0.375rem 0.625rem;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--bg-primary);
		color: var(--text-primary);
		font-size: 0.8125rem;
		box-sizing: border-box;
	}

	.form-input:focus,
	.form-select:focus {
		outline: none;
		border-color: var(--accent);
	}

	.form-section {
		margin-bottom: 1.25rem;
	}

	.section-header {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		margin-bottom: 0.5rem;
		padding-bottom: 0.375rem;
		border-bottom: 1px solid var(--border);
	}

	.section-title {
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--text-muted);
		text-transform: uppercase;
		letter-spacing: 0.05em;
	}

	.btn-bat-import {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		margin-left: auto;
		background: var(--bg-tertiary, #2d2f45);
		border: 1px solid var(--border);
		color: var(--text-secondary);
		font-size: 0.6875rem;
		cursor: pointer;
		padding: 3px 10px;
		border-radius: 4px;
		transition: border-color 0.15s, color 0.15s;
	}

	.btn-bat-import:hover {
		border-color: var(--accent);
		color: var(--text-primary);
	}

	.hidden-input {
		display: none;
	}

	.form-textarea {
		width: 100%;
		padding: 0.5rem 0.625rem;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--bg-primary);
		color: var(--text-primary);
		font-size: 0.8125rem;
		font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, monospace;
		box-sizing: border-box;
		resize: vertical;
		line-height: 1.5;
	}

	.form-textarea:focus {
		outline: none;
		border-color: var(--accent);
	}

	.subnet-count {
		display: block;
		font-size: 0.6875rem;
		color: var(--text-muted);
		margin-top: 0.25rem;
	}

	.field-error .form-input,
	.field-error .form-select,
	.field-error .form-textarea {
		border-color: var(--error, #ef4444);
		box-shadow: 0 0 0 2px rgba(239, 68, 68, 0.15);
	}
</style>
