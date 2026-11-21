<script lang="ts">
	import { onMount } from 'svelte';
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Modal } from '$lib/components/ui';
	import type { CDNDispatcherConfig, CDNRouteRule } from '$lib/types';
	import {
		Network,
		Plus,
		Trash2,
		Save,
		Edit2,
		CheckCircle2,
		XCircle,
		ShieldAlert,
		RefreshCw,
		ExternalLink,
	} from 'lucide-svelte';

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
	let domainError = $state('');

	onMount(() => {
		void loadConfig();
	});

	async function loadConfig() {
		loading = true;
		try {
			config = await api.getDispatcherConfig();
			onStatusChange?.(Boolean(config.running));
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось загрузить настройки Диспетчера');
		} finally {
			loading = false;
		}
	}

	function validateDomain(domain: string): boolean {
		const clean = domain.trim().toLowerCase();
		if (!clean) {
			domainError = 'Укажите доменное имя';
			return false;
		}
		if (clean.includes('/') || clean.includes(':') || clean.startsWith('http')) {
			domainError = 'Укажите только FQDN (например: cdn.example.com)';
			return false;
		}
		const fqdnRegex = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$/;
		if (!fqdnRegex.test(clean)) {
			domainError = 'Некорректный формат доменного имени';
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
				domainError = 'Узел с таким доменом уже существует';
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
			notifications.success('Настройки Диспетчера сохранены');
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка сохранения Диспетчера');
		} finally {
			saving = false;
		}
	}

	function pluralizeDomains(count: number): string {
		const mod100 = Math.abs(count) % 100;
		const mod10 = mod100 % 10;
		if (mod100 >= 11 && mod100 <= 19) return `${count} доменов`;
		if (mod10 === 1) return `${count} домен`;
		if (mod10 >= 2 && mod10 <= 4) return `${count} домена`;
		return `${count} доменов`;
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
					<h2 class="title">Входящие подключения (:{listenPort})</h2>
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
					Маршрутизация входящего трафика на порту :9009 между VLESS (XHTTP) и Прокси Telegram
				</p>
			</div>
		</div>

		<div class="header-actions">
			<Button variant="secondary" size="sm" onclick={loadConfig} disabled={loading}>
				<RefreshCw size={14} class={loading ? 'animate-spin' : ''} />
				Обновить
			</Button>
			<Button variant="primary" size="sm" onclick={openAddModal} disabled={loading}>
				<Plus size={14} />
				Добавить узел
			</Button>
		</div>
	</header>

	{#if loading && !config}
		<div class="loading-state">
			<RefreshCw size={24} class="animate-spin text-muted" />
			<span>Загрузка маршрутов...</span>
		</div>
	{:else if config}
		<!-- Info Hint -->
		<div class="info-box">
			<p class="text-sm">
				<strong>Как работает маршрутизация на :9009:</strong> Входящий трафик от клиентов приходит на порт 9009.
				Шлюз разделяет его по путям: запросы к <strong>VLESS (XHTTP)</strong> идут по пути <code>{config.xray_path_prefix || '/cdn-bridge'}/*</code>,
				а запросы к <strong>Прокси Telegram</strong> — по путям <code>/</code> и <code>/api/*</code>.
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
				<h3 class="section-title">Таблица входящих доменов</h3>
				<span class="count-badge">{pluralizeDomains((config.routes ?? []).length)}</span>
			</div>

			{#if (config.routes ?? []).length === 0}
				<div class="empty-routes">
					<p>Нет зарегистрированных доменов.</p>
					<Button variant="secondary" size="sm" onclick={openAddModal}>
						<Plus size={14} />
						Добавить первый узел
					</Button>
				</div>
			{:else}
				<div class="cdn-table-wrap">
					<table class="routes-table">
						<thead>
							<tr>
								<th>FQDN Домен</th>
								<th>Название / Описание</th>
								<th class="text-center">VLESS (XHTTP)<br><small class="text-xs text-muted">{config.xray_path_prefix || '/cdn-bridge'}/*</small></th>
								<th class="text-center">Прокси Telegram<br><small class="text-xs text-muted">/, /api/*</small></th>
								<th class="text-center">Статус</th>
								<th class="text-right">Действия</th>
							</tr>
						</thead>
						<tbody>
							{#each config.routes as route (route.id)}
								<tr class:row-disabled={!route.enabled}>
									<td class="font-mono font-medium">
										{route.domain}
									</td>
									<td class="text-muted">
										{route.description || '—'}
									</td>
									<td class="text-center">
										{#if route.route_xray}
											<span class="badge badge-route-xray">VLESS (XHTTP)</span>
										{:else}
											<span class="text-muted">—</span>
										{/if}
									</td>
									<td class="text-center">
										{#if route.route_telegram}
											<span class="badge badge-route-tg">Telegram</span>
										{:else}
											<span class="text-muted">—</span>
										{/if}
									</td>
									<td class="text-center">
										<button
											type="button"
											class="toggle-status-btn"
											onclick={() => toggleRouteEnabled(route.id)}
											title={route.enabled ? 'Отключить узел' : 'Включить узел'}
										>
											{#if route.enabled}
												<CheckCircle2 size={18} class="text-success" />
											{:else}
												<XCircle size={18} class="text-muted" />
											{/if}
										</button>
									</td>
									<td class="text-right">
										<div class="row-actions">
											<button
												type="button"
												class="cdn-btn-icon"
												onclick={() => openEditModal(route)}
												title="Редактировать"
											>
												<Edit2 size={15} />
											</button>
											<button
												type="button"
												class="cdn-btn-icon text-danger"
												onclick={() => deleteRoute(route.id)}
												title="Удалить"
											>
												<Trash2 size={15} />
											</button>
										</div>
									</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			{/if}
		</section>

		<!-- General Settings -->
		<section class="section border-top">
			<h3 class="section-title">Параметры безопасности шлюза</h3>
			<div class="settings-grid">
				<div class="setting-item">
					<label for="default-action-select" class="setting-label">Действие для неизвестных доменов:</label>
					<select
						id="default-action-select"
						class="select-input"
						bind:value={config.default_action}
					>
						<option value="404">404 — Страница не найдена (стандартный ответ)</option>
						<option value="reject">403 — Сброс соединения (блокировка сканеров)</option>
					</select>
					<span class="hint">
						Режим сброса немедленно возвращает 403 и закрывает соединение для защиты от неавторизованных сканеров.
					</span>
				</div>
			</div>
		</section>

		<!-- Footer actions -->
		<footer class="footer">
			<Button variant="primary" onclick={saveAllConfig} disabled={saving}>
				<Save size={16} />
				{saving ? 'Сохранение...' : 'Сохранить изменения'}
			</Button>
		</footer>
	{/if}
</div>

<Modal
	bind:open={editModalOpen}
	title={isNewRoute ? 'Добавить узел в Диспетчер' : 'Редактировать узел'}
	onclose={() => editModalOpen = false}
	size="md"
>
	<div class="modal-body">
		<div class="form-group">
			<label for="route-domain" class="form-label">FQDN Домен узла <span class="text-danger">*</span></label>
			<input
				id="route-domain"
				type="text"
				class="cdn-input font-mono"
				placeholder="например: cdn.example.com"
				bind:value={editingRoute.domain}
				oninput={() => domainError = ''}
			/>
			{#if domainError}
				<span class="form-error">{domainError}</span>
			{/if}
			<span class="hint">Указывайте только доменное имя без протокола http:// и без слэшей.</span>
		</div>

		<div class="form-group">
			<label for="route-description" class="form-label">Описание (произвольная метка)</label>
			<input
				id="route-description"
				type="text"
				class="cdn-input"
				placeholder="например: Основной узел CDN"
				bind:value={editingRoute.description}
			/>
		</div>

		<div class="form-group">
			<span class="form-label">Маршрутизация сервисов:</span>
			<label class="checkbox-label">
				<input type="checkbox" bind:checked={editingRoute.route_xray} />
				<span>Проксировать <strong>VLESS (XHTTP)</strong> <small class="text-xs text-muted">({config?.xray_path_prefix || '/cdn-bridge'}/*)</small></span>
			</label>
			<label class="checkbox-label">
				<input type="checkbox" bind:checked={editingRoute.route_telegram} />
				<span>Проксировать <strong>Прокси Telegram</strong> <small class="text-xs text-muted">(веб и API: /, /api/*)</small></span>
			</label>
		</div>

		<div class="form-group">
			<label class="checkbox-label">
				<input type="checkbox" bind:checked={editingRoute.enabled} />
				<span>Узел активен</span>
			</label>
		</div>
	</div>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => editModalOpen = false}>Отмена</Button>
		<Button variant="primary" onclick={saveRouteModal}>
			{isNewRoute ? 'Добавить' : 'Применить'}
		</Button>
	{/snippet}
</Modal>

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

	.badge-route-xray {
		background: var(--color-accent-tint);
		color: var(--color-accent);
		border: 1px solid var(--color-accent-border);
	}

	.badge-route-tg {
		background: var(--color-info-tint);
		color: var(--color-info);
		border: 1px solid var(--color-info-border);
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

	.cdn-table-wrap {
		overflow-x: auto;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
	}

	.routes-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.875rem;
	}

	.routes-table th {
		background: var(--color-bg-tertiary);
		padding: 0.625rem 0.75rem;
		text-align: left;
		font-weight: 500;
		color: var(--color-text-secondary);
		border-bottom: 1px solid var(--color-border);
	}

	.routes-table td {
		padding: 0.625rem 0.75rem;
		color: var(--color-text-primary);
		border-bottom: 1px solid var(--color-border);
		vertical-align: middle;
	}

	.row-disabled {
		opacity: 0.55;
		background: var(--color-bg-tertiary);
	}

	.text-center { text-align: center; }
	.text-right { text-align: right; }
	.text-muted { color: var(--color-text-muted); }
	.text-danger { color: var(--color-error); }

	.toggle-status-btn {
		background: none;
		border: none;
		cursor: pointer;
		padding: 0.25rem;
		display: inline-flex;
	}

	.toggle-status-btn :global(.text-success) {
		color: var(--color-success);
	}

	.toggle-status-btn :global(.text-muted) {
		color: var(--color-text-muted);
	}

	.row-actions {
		display: flex;
		justify-content: flex-end;
		gap: 0.35rem;
	}

	.cdn-btn-icon {
		background: none;
		border: none;
		cursor: pointer;
		padding: 0.3rem;
		border-radius: 0.25rem;
		color: var(--color-text-muted);
	}

	.cdn-btn-icon:hover {
		background: var(--color-bg-hover);
		color: var(--color-text-primary);
	}

	.cdn-btn-icon.text-danger {
		color: var(--color-error);
	}

	.cdn-btn-icon.text-danger:hover {
		background: var(--color-error-tint);
		color: var(--color-error);
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

	.modal-body {
		display: flex;
		flex-direction: column;
		gap: 1rem;
		padding: 0.5rem 0;
	}

	.form-group {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
	}

	.form-label {
		font-size: 0.875rem;
		font-weight: 500;
		color: var(--color-text-primary);
	}

	.cdn-input {
		width: 100%;
		box-sizing: border-box;
		padding: 0.5rem 0.75rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		font-size: 0.875rem;
	}

	.cdn-input:focus {
		outline: none;
		border-color: var(--color-accent);
	}

	.checkbox-label {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 0.875rem;
		color: var(--color-text-primary);
		cursor: pointer;
	}

	.form-error {
		font-size: 0.75rem;
		color: var(--color-error);
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
