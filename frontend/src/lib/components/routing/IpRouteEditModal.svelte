<script lang="ts">
	import type { StaticRouteList, DnsRouteTunnelInfo } from '$lib/types';
	import { Modal } from '$lib/components/ui';

	interface Props {
		open: boolean;
		route: StaticRouteList | null;
		tunnels: DnsRouteTunnelInfo[];
		saving: boolean;
		onsave: (data: { name: string; tunnelID: string; subnets: string[] }) => void;
		onclose: () => void;
	}

	let { open, route, tunnels: rawTunnels, saving, onsave, onclose }: Props = $props();
	let tunnels = $derived((rawTunnels ?? []).filter(t => !t.wan || t.status === 'up'));

	// Form state
	let name = $state('');
	let tunnelID = $state('');
	let subnetsText = $state('');

	// Reset form when modal opens
	$effect(() => {
		if (open) {
			if (route) {
				name = route.name;
				tunnelID = route.tunnelID;
				subnetsText = (route.subnets ?? []).join('\n');
			} else {
				name = '';
				tunnelID = tunnels.length > 0 ? tunnels[0].id : '';
				subnetsText = '';
			}
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

	let userTunnels = $derived(tunnels.filter(t => !t.system));
	let systemTunnels = $derived(tunnels.filter(t => t.system));

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
				// Match "route add X.X.X.X mask Y.Y.Y.Y ..." or CIDR patterns
				const routeMatch = trimmed.match(/route\s+add\s+(\d+\.\d+\.\d+\.\d+)\s+mask\s+(\d+\.\d+\.\d+\.\d+)/i);
				if (routeMatch) {
					const cidr = maskToCidr(routeMatch[1], routeMatch[2]);
					if (cidr) subnets.push(cidr);
					continue;
				}
				// Also accept plain CIDR lines
				const cidrMatch = trimmed.match(/^(\d+\.\d+\.\d+\.\d+\/\d+)$/);
				if (cidrMatch) {
					subnets.push(cidrMatch[1]);
				}
			}
			if (subnets.length > 0) {
				const existing = subnetsText.trim();
				subnetsText = existing ? existing + '\n' + subnets.join('\n') : subnets.join('\n');
			}
		} catch {
			// silently ignore read errors
		}
		// Reset input so the same file can be re-selected
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
		onsave({
			name: name.trim(),
			tunnelID,
			subnets: parsedSubnets,
		});
	}
</script>

<Modal {open} {title} size="lg" onclose={onclose}>
	<!-- Name -->
	<div class="form-group">
		<!-- svelte-ignore a11y_label_has_associated_control -->
		<label class="form-label">Название</label>
		<input
			class="form-input"
			type="text"
			placeholder="Заблокированные подсети"
			value={name}
			oninput={(e) => { name = (e.target as HTMLInputElement).value; }}
		/>
	</div>

	<!-- Tunnel -->
	<div class="form-group">
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
		</select>
	</div>

	<!-- Subnets -->
	<div class="form-section">
		<div class="section-header">
			<div class="section-title">Подсети (по одной на строку, CIDR)</div>
			<button class="btn-bat-import" onclick={handleBatImport}>
				Импорт из .bat
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
	</div>

	{#snippet actions()}
		<button class="btn btn-secondary" onclick={onclose}>Отмена</button>
		<button class="btn btn-primary" onclick={handleSave} disabled={!canSave || saving}>
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
		background: none;
		border: none;
		color: var(--accent);
		font-size: 0.6875rem;
		cursor: pointer;
		padding: 0;
		text-decoration: underline;
	}

	.btn-bat-import:hover {
		opacity: 0.8;
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
</style>
