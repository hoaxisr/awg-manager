<script lang="ts">
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { singboxTunnels, toggleTunnelEnabled } from '$lib/stores/singbox';
	import { notifications } from '$lib/stores/notifications';
	import { PageContainer } from '$lib/components/layout';

	let tag = $derived($page.params.tag!);
	let loading = $state(true);
	let saving = $state(false);
	let error = $state<string | null>(null);
	let outbound = $state<Record<string, any> | null>(null);
	let protocol = $state<string>('');

	let tunnel = $derived($singboxTunnels.data?.find(t => t.tag === tag));
	let enabled = $derived(tunnel?.enabled ?? true);

	onMount(async () => {
		try {
			const r = await api.singboxGetTunnel(tag);
			outbound = r.outbound as Record<string, any>;
			protocol = outbound?.type ?? '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			loading = false;
		}
	});

	async function handleToggle(): Promise<void> {
		if (!tunnel) return;
		try {
			await toggleTunnelEnabled(tag, !enabled);
			enabled = !enabled;
			notifications.show(`Туннель ${enabled ? 'включен' : 'выключен'}`, 'success');
		} catch (e) {
			notifications.show('Ошибка переключения', 'error');
		}
	}

	async function save(): Promise<void> {
		if (!outbound) return;
		saving = true;
		error = null;
		try {
			const fresh = await api.singboxUpdateTunnel(tag, outbound);
			singboxTunnels.applyMutationResponse(fresh);
			goto('/');
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			saving = false;
		}
	}

	function setField(path: string[], value: any): void {
		if (!outbound) return;
		let obj: any = outbound;
		for (let i = 0; i < path.length - 1; i++) {
			if (obj[path[i]] == null) obj[path[i]] = {};
			obj = obj[path[i]];
		}
		obj[path[path.length - 1]] = value;
		outbound = { ...outbound };
	}

	function getField(path: string[]): any {
		let obj: any = outbound;
		for (const p of path) {
			if (obj == null) return undefined;
			obj = obj[p];
		}
		return obj;
	}
</script>

<svelte:head>
	<title>{tag} — Sing-box</title>
</svelte:head>

<PageContainer>
	<div class="sticky-header">
		<div class="header-left">
			<button class="btn btn-ghost btn-sm" onclick={() => goto('/')}>
				<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<path d="M19 12H5M12 19l-7-7 7-7"/>
				</svg>
				Назад
			</button>
			<h1 class="page-title">{tag}</h1>
			{#if protocol}
				<span class="badge-protocol">{protocol}</span>
			{/if}
			<button class="btn btn-sm toggle-btn {enabled ? 'btn-primary' : 'btn-ghost'}" onclick={handleToggle}>
				{enabled ? 'Активен' : 'Отключён'}
			</button>
		</div>
		<button
			class="btn btn-primary"
			onclick={save}
			disabled={saving || !outbound}
		>
			{saving ? 'Сохранение...' : 'Сохранить'}
		</button>
	</div>

	{#if loading}
		<div class="py-12 text-center text-surface-400">Загрузка...</div>
	{:else if !outbound}
		<div class="py-12 text-center text-error-500">{error ?? 'Туннель не найден'}</div>
	{:else}
		<form onsubmit={(e) => { e.preventDefault(); save(); }}>
			<div class="section">
				<h2 class="section-title">Основные параметры</h2>

				<div class="form-group">
					<label class="label" for="server">Сервер</label>
					<input
						id="server"
						class="input"
						value={outbound.server ?? ''}
						oninput={(e) => setField(['server'], (e.target as HTMLInputElement).value)}
					/>
				</div>

				<div class="form-group">
					<label class="label" for="server_port">Порт</label>
					<input
						id="server_port"
						class="input"
						type="number"
						value={outbound.server_port ?? 0}
						oninput={(e) => setField(['server_port'], parseInt((e.target as HTMLInputElement).value, 10))}
					/>
				</div>
			</div>

			{#if protocol === 'vless'}
				<div class="section">
					<h2 class="section-title">VLESS</h2>

					<div class="form-group">
						<label class="label" for="uuid">UUID</label>
						<input
							id="uuid"
							class="input"
							value={outbound.uuid ?? ''}
							oninput={(e) => setField(['uuid'], (e.target as HTMLInputElement).value)}
						/>
					</div>

					<div class="form-group">
						<label class="label" for="flow">Flow</label>
						<input
							id="flow"
							class="input"
							value={outbound.flow ?? ''}
							oninput={(e) => setField(['flow'], (e.target as HTMLInputElement).value)}
						/>
					</div>
				</div>

				<div class="section">
					<h2 class="section-title">TLS</h2>

					<div class="form-group">
						<label class="label" for="sni">SNI</label>
						<input
							id="sni"
							class="input"
							value={getField(['tls', 'server_name']) ?? ''}
							oninput={(e) => setField(['tls', 'server_name'], (e.target as HTMLInputElement).value)}
						/>
					</div>

					<div class="form-group">
						<label class="label" for="fingerprint">Fingerprint</label>
						<select
							id="fingerprint"
							class="input"
							value={getField(['tls', 'utls', 'fingerprint']) ?? ''}
							onchange={(e) => setField(['tls', 'utls', 'fingerprint'], (e.target as HTMLSelectElement).value)}
						>
							<option value="">—</option>
							<option value="chrome">chrome</option>
							<option value="firefox">firefox</option>
							<option value="safari">safari</option>
							<option value="edge">edge</option>
						</select>
					</div>

					{#if getField(['tls', 'reality'])}
						<h3 class="subsection-title">Reality</h3>

						<div class="form-group">
							<label class="label" for="reality_pubkey">Public Key</label>
							<input
								id="reality_pubkey"
								class="input"
								value={getField(['tls', 'reality', 'public_key']) ?? ''}
								oninput={(e) => setField(['tls', 'reality', 'public_key'], (e.target as HTMLInputElement).value)}
							/>
						</div>

						<div class="form-group">
							<label class="label" for="reality_short_id">Short ID</label>
							<input
								id="reality_short_id"
								class="input"
								value={getField(['tls', 'reality', 'short_id']) ?? ''}
								oninput={(e) => setField(['tls', 'reality', 'short_id'], (e.target as HTMLInputElement).value)}
							/>
						</div>
					{/if}
				</div>

				{#if outbound.transport?.type === 'grpc'}
					<div class="section">
						<h2 class="section-title">Transport (gRPC)</h2>

						<div class="form-group">
							<label class="label" for="grpc_service">Service Name</label>
							<input
								id="grpc_service"
								class="input"
								value={getField(['transport', 'service_name']) ?? ''}
								oninput={(e) => setField(['transport', 'service_name'], (e.target as HTMLInputElement).value)}
							/>
						</div>
					</div>
				{/if}

				{#if outbound.transport?.type === 'ws'}
					<div class="section">
						<h2 class="section-title">Transport (WebSocket)</h2>
						<p class="section-hint">Параметры импортированы из ссылки и редактированию не подлежат.</p>

						<div class="form-group">
							<label class="label" for="ws_path">Path</label>
							<input id="ws_path" class="input" value={getField(['transport', 'path']) ?? '/'} readonly />
						</div>

						{#if getField(['transport', 'headers', 'Host'])}
							<div class="form-group">
								<label class="label" for="ws_host">Host header</label>
								<input id="ws_host" class="input" value={getField(['transport', 'headers', 'Host'])} readonly />
							</div>
						{/if}

						{#if getField(['transport', 'early_data_header_name'])}
							<div class="form-group">
								<label class="label" for="ws_ed">Early Data Header</label>
								<input id="ws_ed" class="input" value={getField(['transport', 'early_data_header_name'])} readonly />
							</div>
						{/if}
					</div>
				{/if}

			{:else if protocol === 'hysteria2'}
				<div class="section">
					<h2 class="section-title">Hysteria2</h2>

					<div class="form-group">
						<label class="label" for="password">Пароль</label>
						<input
							id="password"
							class="input"
							type="password"
							value={outbound.password ?? ''}
							oninput={(e) => setField(['password'], (e.target as HTMLInputElement).value)}
						/>
					</div>
				</div>

				<div class="section">
					<h2 class="section-title">TLS</h2>

					<div class="form-group">
						<label class="label" for="hy2_sni">SNI</label>
						<input
							id="hy2_sni"
							class="input"
							value={getField(['tls', 'server_name']) ?? ''}
							oninput={(e) => setField(['tls', 'server_name'], (e.target as HTMLInputElement).value)}
						/>
					</div>

					<label class="checkbox-label">
						<input
							type="checkbox"
							checked={getField(['tls', 'insecure']) ?? false}
							onchange={(e) => setField(['tls', 'insecure'], (e.target as HTMLInputElement).checked)}
						/>
						<span>Insecure (пропустить проверку сертификата)</span>
					</label>
				</div>

			{:else if protocol === 'naive'}
				<div class="section">
					<h2 class="section-title">NaiveProxy</h2>

					<div class="form-group">
						<label class="label" for="username">Пользователь</label>
						<input
							id="username"
							class="input"
							value={outbound.username ?? ''}
							oninput={(e) => setField(['username'], (e.target as HTMLInputElement).value)}
						/>
					</div>

					<div class="form-group">
						<label class="label" for="naive_password">Пароль</label>
						<input
							id="naive_password"
							class="input"
							type="password"
							value={outbound.password ?? ''}
							oninput={(e) => setField(['password'], (e.target as HTMLInputElement).value)}
						/>
					</div>
				</div>
			{/if}

			{#if error}
				<div class="error-msg">{error}</div>
			{/if}

			<div class="form-actions">
				<button type="button" class="btn btn-secondary btn-sm" onclick={() => goto('/')}>Отмена</button>
				<button type="submit" class="btn btn-primary btn-sm" disabled={saving}>
					{saving ? 'Сохранение...' : 'Сохранить'}
				</button>
			</div>
		</form>
	{/if}
</PageContainer>

<style>
	.sticky-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		position: sticky;
		top: 0;
		z-index: 10;
		background: var(--bg-primary);
		padding: 0.75rem 0;
		margin-bottom: 1rem;
		border-bottom: 1px solid var(--border);
	}

	.header-left {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}

	.page-title {
		font-size: 1.25rem;
		font-weight: 600;
		margin: 0;
	}

	.badge-protocol {
		display: inline-flex;
		align-items: center;
		padding: 2px 8px;
		font-size: 0.6875rem;
		font-weight: 500;
		border-radius: 9999px;
		background: rgba(148, 163, 184, 0.15);
		color: var(--text-muted);
	}

	.toggle-btn {
		transition: all 0.2s;
	}

	.section {
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 8px;
		padding: 1.25rem;
		margin-bottom: 1rem;
	}

	.section-title {
		font-size: 1rem;
		font-weight: 600;
		margin: 0 0 1rem;
	}

	.section-hint {
		font-size: 12px;
		color: var(--text-muted);
		margin: -0.5rem 0 1rem;
	}

	.subsection-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--text-secondary);
		margin: 16px 0 8px;
	}

	.form-group {
		display: flex;
		flex-direction: column;
		gap: 6px;
		margin-bottom: 12px;
	}

	.form-group:last-child {
		margin-bottom: 0;
	}

	.label {
		font-size: 13px;
		font-weight: 500;
		color: var(--text-secondary);
	}

	.input {
		padding: 8px 12px;
		font-size: 13px;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 6px;
		color: var(--text-primary);
		transition: border-color 0.15s;
	}

	.input:focus {
		outline: none;
		border-color: var(--accent);
	}

	.input[type="number"] {
		-moz-appearance: textfield;
		appearance: textfield;
	}

	.input[type="number"]::-webkit-outer-spin-button,
	.input[type="number"]::-webkit-inner-spin-button {
		-webkit-appearance: none;
		margin: 0;
	}

	.checkbox-label {
		display: inline-flex;
		align-items: center;
		gap: 8px;
		font-size: 13px;
		color: var(--text-primary);
		cursor: pointer;
	}

	.checkbox-label input[type="checkbox"] {
		accent-color: var(--accent);
	}

	.error-msg {
		color: var(--error);
		font-size: 13px;
		margin-bottom: 1rem;
	}

	.form-actions {
		display: flex;
		gap: 8px;
		justify-content: flex-end;
		margin-top: 1rem;
	}

	@media (max-width: 640px) {
		.sticky-header {
			flex-direction: column;
			gap: 0.75rem;
			align-items: stretch;
		}

		.header-left {
			flex-wrap: wrap;
		}
	}
</style>
