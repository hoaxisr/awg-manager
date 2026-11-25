<script lang="ts">
	import { onMount } from 'svelte';
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Button } from '$lib/components/ui';
	import type { CDNDispatcherConfig, CDNRouteRule } from '$lib/types';
	import CDNRoutesTable from './CDNRoutesTable.svelte';
	import CDNRouteEditModal, { type DomainError } from './CDNRouteEditModal.svelte';
	import { Network, Plus, Save, RefreshCw } from 'lucide-svelte';

	interface Props {
		onStatusChange?: (running: boolean) => void;
	}

	let { onStatusChange }: Props = $props();

	let config = $state<CDNDispatcherConfig | null>(null);
	let loading = $state(true);
	let saving = $state(false);

	let listenPort = $derived.by(() => {
		const raw = config?.listen_addr?.trim() || ':9009';
		const idx = raw.lastIndexOf(':');
		return idx >= 0 ? raw.slice(idx + 1) || '9009' : raw;
	});
	let isRunning = $derived(Boolean(config?.running));
	let xrayPathPrefix = $derived(config?.xray_path_prefix || '/cdn-bridge');

	// Modal states
	let editModalOpen = $state(false);
	let isNewRoute = $state(false);
	let editingRoute = $state<CDNRouteRule>({
		id: '',
		domain: '',
		description: '',
		route_xray: true,
		route_telegram: true,
		enabled: true,
	});
	let domainError = $state<DomainError>('');

	onMount(() => {
		void loadConfig();
	});

	async function loadConfig() {
		loading = true;
		try {
			config = await api.getDispatcherConfig();
			onStatusChange?.(Boolean(config.running));
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_cdn_load_error());
		} finally {
			loading = false;
		}
	}

	function validateDomain(domain: string): boolean {
		const clean = domain.trim().toLowerCase();
		if (!clean) {
			domainError = 'empty';
			return false;
		}
		if (clean.includes('/') || clean.includes(':') || clean.startsWith('http')) {
			domainError = 'fqdn';
			return false;
		}
		const fqdnRegex = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$/;
		if (!fqdnRegex.test(clean)) {
			domainError = 'format';
			return false;
		}
		domainError = '';
		return true;
	}

	function openAddModal() {
		isNewRoute = true;
		editingRoute = {
			id: 'route-' + Date.now(),
			domain: '',
			description: '',
			route_xray: true,
			route_telegram: true,
			enabled: true,
		};
		domainError = '';
		editModalOpen = true;
	}

	function openEditModal(rule: CDNRouteRule) {
		isNewRoute = false;
		editingRoute = { ...rule };
		domainError = '';
		editModalOpen = true;
	}

	function saveRouteModal() {
		if (!validateDomain(editingRoute.domain)) {
			return;
		}
		if (!config) return;

		const normDomain = editingRoute.domain.trim().toLowerCase();
		editingRoute.domain = normDomain;

		const routes = [...(config.routes ?? [])];
		if (isNewRoute) {
			// Check duplicate
			if (routes.some((r) => r.domain.toLowerCase() === normDomain)) {
				domainError = 'duplicate';
				return;
			}
			routes.push({ ...editingRoute });
		} else {
			const idx = routes.findIndex((r) => r.id === editingRoute.id);
			if (idx !== -1) {
				routes[idx] = { ...editingRoute };
			}
		}

		config.routes = routes;
		editModalOpen = false;
	}

	function deleteRoute(id: string) {
		if (!config) return;
		config.routes = (config.routes ?? []).filter((r) => r.id !== id);
	}

	function toggleRouteEnabled(id: string) {
		if (!config) return;
		config.routes = (config.routes ?? []).map((r) => {
			if (r.id === id) {
				return { ...r, enabled: !r.enabled };
			}
			return r;
		});
	}

	async function saveAllConfig() {
		if (!config) return;
		saving = true;
		try {
			const updated = await api.updateDispatcherConfig(config);
			config = updated;
			onStatusChange?.(Boolean(updated.running));
			if (updated.warning) {
				notifications.warning(updated.warning);
			} else {
				notifications.success(m.servers_cdn_saved());
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_cdn_save_error());
		} finally {
			saving = false;
		}
	}
</script>

<div class="card-root">
	<header class="header">
		<div class="header-left">
			<div class="icon-wrap">
				<Network size={22} />
			</div>
			<div>
				<div class="title-row">
					<h2 class="title">{m.servers_cdn_title({ port: listenPort })}</h2>
					{#if config}
						<span
							class="badge"
							class:badge-success={isRunning}
							class:badge-muted={!isRunning}
							data-testid="dispatcher-status-badge"
						>
							{isRunning
								? m.servers_dispatcher_status_running({ port: listenPort })
								: m.servers_dispatcher_status_stopped({ port: listenPort })}
						</span>
					{/if}
				</div>
				<p class="subtitle">
					{m.servers_cdn_subtitle()}
				</p>
			</div>
		</div>

		<div class="header-actions">
			<Button variant="secondary" size="sm" onclick={loadConfig} disabled={loading}>
				<RefreshCw size={14} class={loading ? 'animate-spin' : ''} />
				{m.common_refresh()}
			</Button>
			<Button variant="primary" size="sm" onclick={openAddModal} disabled={loading}>
				<Plus size={14} />
				{m.servers_cdn_add_route()}
			</Button>
		</div>
	</header>

	{#if loading && !config}
		<div class="loading-state">
			<RefreshCw size={24} class="animate-spin text-muted" />
			<span>{m.servers_cdn_loading()}</span>
		</div>
	{:else if config}
		<!-- Info Hint -->
		<div class="info-box">
			<p class="text-sm">
				<strong>{m.servers_cdn_info_title()}</strong> {m.servers_cdn_info_intro()}
				{m.servers_cdn_info_xray_before()} <strong>VLESS (XHTTP)</strong> {m.servers_cdn_info_xray_path()} <code>{xrayPathPrefix}/*</code>,
				{m.servers_cdn_info_tg_before()} <strong>{m.servers_cdn_tg_proxy()}</strong> — {m.servers_cdn_info_tg_paths()} <code>/</code> {m.servers_cdn_info_and()} <code>/api/*</code>.
			</p>
		</div>

		<div
			class="status-note"
			class:status-note-running={isRunning}
			class:status-note-stopped={!isRunning}
			data-testid="dispatcher-status-note"
		>
			<p class="text-sm">
				{isRunning
					? m.servers_dispatcher_running_hint({ port: listenPort })
					: m.servers_dispatcher_stopped_hint({ port: listenPort })}
			</p>
		</div>

		<!-- Routes Table -->
		<section class="section">
			<div class="section-header">
				<h3 class="section-title">{m.servers_cdn_routes_title()}</h3>
				<span class="count-badge">{m.servers_cdn_domains_count({ count: (config.routes ?? []).length })}</span>
			</div>

			{#if (config.routes ?? []).length === 0}
				<div class="empty-routes">
					<p>{m.servers_cdn_routes_empty()}</p>
					<Button variant="secondary" size="sm" onclick={openAddModal}>
						<Plus size={14} />
						{m.servers_cdn_add_first_route()}
					</Button>
				</div>
			{:else}
				<CDNRoutesTable
					routes={config.routes ?? []}
					{xrayPathPrefix}
					ontoggle={toggleRouteEnabled}
					onedit={openEditModal}
					ondelete={deleteRoute}
				/>
			{/if}
		</section>

		<!-- General Settings -->
		<section class="section border-top">
			<h3 class="section-title">{m.servers_cdn_security_title()}</h3>
			<div class="settings-grid">
				<div class="setting-item">
					<label for="default-action-select" class="setting-label">{m.servers_cdn_default_action_label()}</label>
					<select
						id="default-action-select"
						class="select-input"
						bind:value={config.default_action}
					>
						<option value="404">{m.servers_cdn_default_action_404()}</option>
						<option value="reject">{m.servers_cdn_default_action_reject()}</option>
					</select>
					<span class="hint">
						{m.servers_cdn_default_action_hint()}
					</span>
				</div>
			</div>
		</section>

		<!-- Footer actions -->
		<footer class="footer">
			<Button variant="primary" onclick={saveAllConfig} disabled={saving}>
				<Save size={16} />
				{saving ? m.common_saving() : m.servers_cdn_save_changes()}
			</Button>
		</footer>
	{/if}
</div>

<CDNRouteEditModal
	bind:open={editModalOpen}
	isNew={isNewRoute}
	bind:route={editingRoute}
	bind:domainError
	{xrayPathPrefix}
	onsave={saveRouteModal}
/>

<style>
	.card-root {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		padding: 1.5rem;
		color: var(--color-text-primary);
		box-shadow: var(--shadow);
	}

	.header {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		flex-wrap: wrap;
		gap: 1rem;
		margin-bottom: 1.5rem;
	}

	.header-left {
		display: flex;
		gap: 0.75rem;
		align-items: flex-start;
		min-width: 0;
		flex: 1 1 260px;
	}

	.header-left > div {
		min-width: 0;
	}

	.icon-wrap {
		flex-shrink: 0;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		padding: 0.625rem;
		background: var(--color-accent-tint);
		border: 1px solid var(--color-accent-border);
		border-radius: var(--radius-sm);
		color: var(--color-accent);
	}

	.title-row {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.5rem;
	}

	.title {
		font-size: 1.25rem;
		font-weight: 600;
		color: var(--color-text-primary);
		margin: 0;
	}

	.subtitle {
		font-size: 0.875rem;
		color: var(--color-text-secondary);
		margin: 0.25rem 0 0 0;
	}

	.header-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		flex-shrink: 0;
	}

	.info-box {
		padding: 0.75rem 1rem;
		margin-bottom: 1rem;
		border-radius: var(--radius-sm);
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
		line-height: 1.45;
	}

	.info-box p {
		margin: 0;
	}

	.info-box strong {
		color: var(--color-text-primary);
	}

	.info-box code {
		padding: 0.1rem 0.35rem;
		border-radius: 4px;
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		font-family: var(--font-mono);
		font-size: 0.8em;
	}

	.badge {
		font-size: 0.75rem;
		padding: 0.2rem 0.5rem;
		border-radius: 9999px;
		font-weight: 500;
		white-space: nowrap;
	}

	.badge-success {
		background: var(--color-success-tint);
		color: var(--color-success);
		border: 1px solid var(--color-success-border);
	}

	.badge-muted {
		background: var(--color-muted-tint);
		color: var(--color-text-secondary);
		border: 1px solid var(--color-muted-border);
	}

	.status-note {
		padding: 0.7rem 1rem;
		margin-bottom: 1rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		line-height: 1.45;
	}

	.status-note p {
		margin: 0;
	}

	.status-note-stopped {
		background: var(--color-muted-tint);
		border-color: var(--color-muted-border);
		color: var(--color-text-secondary);
	}

	.status-note-running {
		background: var(--color-success-tint);
		border-color: var(--color-success-border);
		color: var(--color-text-primary);
	}

	.section {
		margin-top: 1.25rem;
	}

	.border-top {
		border-top: 1px solid var(--color-border);
		padding-top: 1.25rem;
	}

	.section-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.5rem;
		margin-bottom: 0.75rem;
	}

	.section-title {
		font-size: 0.95rem;
		font-weight: 600;
		color: var(--color-text-primary);
		margin: 0;
	}

	.count-badge {
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}

	.empty-routes {
		padding: 2rem;
		text-align: center;
		border: 1px dashed var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		color: var(--color-text-secondary);
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.75rem;
	}

	.empty-routes p {
		margin: 0;
	}

	.settings-grid {
		display: grid;
		grid-template-columns: 1fr;
		gap: 1rem;
		margin-top: 0.75rem;
	}

	.setting-item {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
	}

	.setting-label {
		font-size: 0.875rem;
		font-weight: 500;
		color: var(--color-text-primary);
	}

	.select-input {
		width: 100%;
		max-width: 400px;
		padding: 0.45rem 0.625rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		box-sizing: border-box;
	}

	.select-input:focus {
		outline: none;
		border-color: var(--color-accent);
	}

	.hint {
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}

	.footer {
		margin-top: 1.5rem;
		display: flex;
		justify-content: flex-end;
	}

	.loading-state {
		display: flex;
		align-items: center;
		justify-content: center;
		gap: 0.75rem;
		padding: 3rem 0;
		color: var(--color-text-muted);
	}

	@media (max-width: 640px) {
		.card-root {
			padding: 1rem;
		}
	}
</style>
