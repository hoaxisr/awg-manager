<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type SystemServiceItem } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Card, Modal, ConfirmModal } from '$lib/components/ui';
	import { errorMessage } from '$lib/utils/errorMessage';
	import { stripAnsi } from '$lib/utils/ansi';
	import {
		RefreshCw,
		Play,
		Square,
		RotateCw,
		Plus,
		FileCode,
		Copy,
		Trash2,
		Search,
		Cpu,
		Layers,
		Check,
		AlertTriangle,
	} from 'lucide-svelte';

	let items = $state<SystemServiceItem[]>([]);
	let loading = $state(false);
	let acting = $state<string | null>(null);
	let searchQuery = $state('');

	// Action confirmation for managed services (like awg-manager)
	let pendingAction = $state<{ item: SystemServiceItem; action: 'start' | 'stop' | 'restart' } | null>(null);

	// Create Service Modal state
	let showCreateModal = $state(false);
	type CreateMode = 'template' | 'clone' | 'custom';
	let createMode = $state<CreateMode>('template');

	// Template fields
	let tplName = $state('');
	let tplPriority = $state(90);
	let tplDesc = $state('');
	let tplProc = $state('');
	let tplArgs = $state('');

	// Clone fields
	let cloneSourceScript = $state('');
	let cloneTargetName = $state('');
	let clonePriority = $state(90);

	// Custom script fields
	let customScriptName = $state('');
	let customScriptContent = $state('');
	let creating = $state(false);

	// Edit Script Modal state
	let editingItem = $state<SystemServiceItem | null>(null);
	let editContent = $state('');
	let editLoading = $state(false);
	let editSaving = $state(false);

	// Delete Service state
	let deletingItem = $state<SystemServiceItem | null>(null);
	let deleting = $state(false);

	onMount(load);

	async function load() {
		loading = true;
		try {
			items = await api.systemServicesList();
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось загрузить службы'));
		} finally {
			loading = false;
		}
	}

	function requestAction(item: SystemServiceItem, action: 'start' | 'stop' | 'restart') {
		if (item.managed && action === 'stop') {
			pendingAction = { item, action };
			return;
		}
		void runAction(item, action);
	}

	async function runAction(item: SystemServiceItem, action: 'start' | 'stop' | 'restart') {
		acting = item.script;
		try {
			const res = await api.systemServicesAction(item.script, action);
			if (res.ok) {
				notifications.success(`${item.name}: ${action}`);
			} else {
				notifications.error(stripAnsi(res.error || res.output || 'Ошибка'));
			}
			await load();
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось выполнить действие'));
		} finally {
			acting = null;
			pendingAction = null;
		}
	}

	function statusHint(item: SystemServiceItem): string {
		return stripAnsi(item.statusText || '');
	}

	const filteredItems = $derived.by(() => {
		const q = searchQuery.trim().toLowerCase();
		if (!q) return items;
		return items.filter((i) => i.name.toLowerCase().includes(q) || i.script.toLowerCase().includes(q));
	});

	// Dynamic generated script preview for template mode
	const generatedTemplateScript = $derived.by(() => {
		const procName = tplProc.trim() || tplName.trim() || 'my-daemon';
		const desc = tplDesc.trim() || tplName.trim() || 'Custom Entware Service';
		const args = tplArgs.trim();

		return `#!/bin/sh

ENABLED=yes
PROCS="${procName}"
ARGS="${args}"
PREARGS=""
DESC="${desc}"
PATH=/opt/sbin:/opt/bin:/opt/usr/bin:/usr/sbin:/usr/bin:/sbin:/bin

. /opt/etc/init.d/rc.func
`;
	});

	function openCreateModal(mode: CreateMode = 'template', donor?: SystemServiceItem) {
		createMode = mode;
		tplName = '';
		tplPriority = 90;
		tplDesc = '';
		tplProc = '';
		tplArgs = '';

		if (donor) {
			cloneSourceScript = donor.script;
			cloneTargetName = donor.name + '-copy';
			clonePriority = 90;
		} else if (items.length > 0) {
			cloneSourceScript = items[0].script;
			cloneTargetName = '';
			clonePriority = 90;
		}

		customScriptName = 'S90custom-service';
		customScriptContent = `#!/bin/sh

ENABLED=yes
PROCS="my-daemon"
ARGS=""
PREARGS=""
DESC="My Custom Service"
PATH=/opt/sbin:/opt/bin:/opt/usr/bin:/usr/sbin:/usr/bin:/sbin:/bin

. /opt/etc/init.d/rc.func
`;
		showCreateModal = true;
	}

	async function handleCreateService() {
		let scriptName = '';
		let content = '';

		if (createMode === 'template') {
			const clean = tplName.trim().replace(/[^a-zA-Z0-9._-]/g, '');
			if (!clean) {
				notifications.error('Укажите корректное имя службы');
				return;
			}
			const prio = Math.min(99, Math.max(10, Number(tplPriority) || 90));
			scriptName = `S${prio}${clean}`;
			content = generatedTemplateScript;
		} else if (createMode === 'clone') {
			const clean = cloneTargetName.trim().replace(/[^a-zA-Z0-9._-]/g, '');
			if (!clean) {
				notifications.error('Укажите имя для новой службы');
				return;
			}
			const prio = Math.min(99, Math.max(10, Number(clonePriority) || 90));
			scriptName = `S${prio}${clean}`;

			// Fetch donor content
			try {
				const src = await api.systemServicesGet(cloneSourceScript);
				content = src.content;
			} catch (e) {
				notifications.error(errorMessage(e, 'Не удалось прочитать исходную службу'));
				return;
			}
		} else {
			// Custom
			scriptName = customScriptName.trim();
			if (!scriptName.startsWith('S') || scriptName.length < 4) {
				notifications.error('Имя скрипта должно начинаться с S и номера, например: S90my-service');
				return;
			}
			content = customScriptContent;
		}

		creating = true;
		try {
			await api.systemServicesSave({ scriptName, content });
			notifications.success(`Служба ${scriptName} успешно создана!`);
			showCreateModal = false;
			await load();
		} catch (e) {
			notifications.error(errorMessage(e, 'Ошибка создания службы'));
		} finally {
			creating = false;
		}
	}

	async function openEditModal(item: SystemServiceItem) {
		editingItem = item;
		editContent = '';
		editLoading = true;
		try {
			const res = await api.systemServicesGet(item.script);
			editContent = res.content;
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось загрузить содержимое скрипта'));
			editingItem = null;
		} finally {
			editLoading = false;
		}
	}

	async function handleSaveEdit(restartAfter = false) {
		if (!editingItem) return;
		editSaving = true;
		const scriptName = editingItem.script.split('/').pop() || editingItem.name;
		try {
			await api.systemServicesSave({ scriptName, content: editContent });
			notifications.success(`Скрипт ${scriptName} сохранен`);
			if (restartAfter) {
				await runAction(editingItem, 'restart');
			}
			editingItem = null;
			await load();
		} catch (e) {
			notifications.error(errorMessage(e, 'Ошибка сохранения скрипта'));
		} finally {
			editSaving = false;
		}
	}

	async function handleDeleteService() {
		if (!deletingItem) return;
		deleting = true;
		try {
			await api.systemServicesDelete(deletingItem.script);
			notifications.success(`Служба ${deletingItem.name} удалена`);
			deletingItem = null;
			await load();
		} catch (e) {
			notifications.error(errorMessage(e, 'Ошибка удаления службы'));
		} finally {
			deleting = false;
		}
	}
</script>

<div class="services-root">
	<Card padding="sm">
		<div class="head-toolbar">
			<div class="toolbar-left">
				<div class="panel-title-wrap">
					<Layers size={18} class="text-accent" />
					<h3>Службы Entware (init.d)</h3>
					<span class="count-badge">{items.length}</span>
				</div>

				<Button size="sm" variant="primary" onclick={() => openCreateModal('template')}>
					{#snippet iconBefore()}<Plus size={14} />{/snippet}
					Добавить службу
				</Button>
			</div>

			<div class="toolbar-right">
				<div class="search-box">
					<Search size={13} class="search-icon" />
					<input
						type="text"
						placeholder="Фильтр по имени или пути…"
						bind:value={searchQuery}
					/>
				</div>

				<Button size="sm" variant="ghost" onclick={load} disabled={loading}>
					{#snippet iconBefore()}<RefreshCw size={14} class={loading ? 'spin' : ''} />{/snippet}
					Обновить
				</Button>
			</div>
		</div>
	</Card>

	<!-- Services List -->
	<Card padding="sm">
		{#if loading && items.length === 0}
			<div class="empty-state">
				<RefreshCw size={24} class="spin" />
				<p>Загрузка списка служб…</p>
			</div>
		{:else if filteredItems.length === 0}
			<div class="empty-state">
				<Search size={24} class="muted" />
				<p>Службы не найдены по запросу «{searchQuery}»</p>
			</div>
		{:else}
			<div class="table-wrap">
				<table class="svc-table">
					<thead>
						<tr>
							<th style="width: 28%;">Служба</th>
							<th style="width: 26%;">Статус</th>
							<th style="text-align: right; width: 46%;">Действия</th>
						</tr>
					</thead>
					<tbody>
						{#each filteredItems as item (item.script)}
							<tr class:is-managed={item.managed} class:is-acting={acting === item.script}>
								<!-- Name & script -->
								<td class="col-name">
									<div class="name-cell-wrap">
										<div class="name-line">
											<span class="svc-name">{item.name}</span>
											{#if item.managed}
												<span class="badge-managed" title={item.managedHint}>Система</span>
											{/if}
										</div>
										<code class="svc-path" title={item.script}>{item.script}</code>
									</div>
								</td>

								<!-- Status -->
								<td class="col-status">
									<div class="status-cell-wrap">
										<span class="status-pill" class:running={item.running}>
											<span class="dot"></span>
											<span>{item.running ? 'Запущен' : 'Остановлен'}</span>
										</span>
										{#if statusHint(item)}
											<span class="status-hint-text" title={statusHint(item)}>{statusHint(item)}</span>
										{/if}
									</div>
								</td>

								<!-- Actions -->
								<td class="col-actions">
									<div class="action-buttons-group">
										<!-- Start -->
										<button
											type="button"
											class="btn-act btn-start"
											disabled={acting === item.script || item.running}
											title="Запустить службу"
											onclick={() => requestAction(item, 'start')}
										>
											<Play size={12} />
											<span>Старт</span>
										</button>

										<!-- Stop -->
										<button
											type="button"
											class="btn-act btn-stop"
											disabled={acting === item.script || !item.running}
											title="Остановить службу"
											onclick={() => requestAction(item, 'stop')}
										>
											<Square size={12} />
											<span>Стоп</span>
										</button>

										<!-- Restart -->
										<button
											type="button"
											class="btn-act btn-restart"
											disabled={acting === item.script}
											title="Перезапустить службу"
											onclick={() => requestAction(item, 'restart')}
										>
											<RotateCw size={12} class={acting === item.script ? 'spin' : ''} />
											<span>Рестарт</span>
										</button>

										<!-- Edit Code -->
										<button
											type="button"
											class="btn-act btn-edit"
											title="Просмотреть / Редактировать скрипт"
											onclick={() => openEditModal(item)}
										>
											<FileCode size={12} />
											<span>Скрипт</span>
										</button>

										<!-- Clone -->
										<button
											type="button"
											class="btn-act btn-clone"
											title="Клонировать эту службу"
											onclick={() => openCreateModal('clone', item)}
										>
											<Copy size={12} />
										</button>

										<!-- Delete -->
										{#if !item.managed && item.name !== 'awg-manager'}
											<button
												type="button"
												class="btn-act btn-delete"
												title="Удалить службу с роутера"
												onclick={() => (deletingItem = item)}
											>
												<Trash2 size={12} />
											</button>
										{/if}
									</div>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
	</Card>
</div>

<!-- Modal: Create New Service -->
<Modal
	open={showCreateModal}
	title="Создание новой службы Entware"
	size="lg"
	onclose={() => (showCreateModal = false)}
>
	<div class="create-modal-root">
		<!-- Tabs -->
		<div class="modal-mode-tabs">
			<button
				type="button"
				class="mode-tab-btn"
				class:active={createMode === 'template'}
				onclick={() => (createMode = 'template')}
			>
				<Cpu size={14} />
				<span>Конструктор (по шаблону)</span>
			</button>
			<button
				type="button"
				class="mode-tab-btn"
				class:active={createMode === 'clone'}
				onclick={() => (createMode = 'clone')}
			>
				<Copy size={14} />
				<span>Клонировать службу</span>
			</button>
			<button
				type="button"
				class="mode-tab-btn"
				class:active={createMode === 'custom'}
				onclick={() => (createMode = 'custom')}
			>
				<FileCode size={14} />
				<span>Свой bash-скрипт</span>
			</button>
		</div>

		{#if createMode === 'template'}
			<!-- Template Mode -->
			<div class="form-grid">
				<div class="form-row">
					<label class="form-field">
						<span class="field-label">Имя службы <span class="required">*</span>:</span>
						<input
							type="text"
							placeholder="например: my-proxy, qwdtt, xray"
							bind:value={tplName}
						/>
						<span class="field-hint">Используется в названии скрипта: <code>/opt/etc/init.d/S{tplPriority || 90}{tplName || 'name'}</code></span>
					</label>

					<label class="form-field" style="max-width: 140px;">
						<span class="field-label">Приоритет (S10-S99):</span>
						<input
							type="number"
							min="10"
							max="99"
							bind:value={tplPriority}
						/>
						<span class="field-hint">Порядок автозапуска</span>
					</label>
				</div>

				<div class="form-row">
					<label class="form-field">
						<span class="field-label">Имя процесса / бинарника:</span>
						<input
							type="text"
							placeholder={tplName ? tplName : 'например: qwdtt или /opt/bin/sing-box'}
							bind:value={tplProc}
						/>
						<span class="field-hint">Переменная PROCS для отслеживания PID через rc.func</span>
					</label>

					<label class="form-field">
						<span class="field-label">Описание службы:</span>
						<input
							type="text"
							placeholder="например: Мой прокси-сервер"
							bind:value={tplDesc}
						/>
					</label>
				</div>

				<label class="form-field">
					<span class="field-label">Аргументы и ключи запуска (ARGS):</span>
					<input
						type="text"
						placeholder="например: -c /opt/etc/config.json -log /opt/var/log/my.log"
						bind:value={tplArgs}
					/>
				</label>

				<!-- Preview -->
				<div class="preview-box">
					<span class="preview-label">Сгенерированный init.d скрипт:</span>
					<pre class="code-preview"><code>{generatedTemplateScript}</code></pre>
				</div>
			</div>
		{:else if createMode === 'clone'}
			<!-- Clone Mode -->
			<div class="form-grid">
				<label class="form-field">
					<span class="field-label">Выберите исходную службу-донор:</span>
					<select bind:value={cloneSourceScript}>
						{#each items as it}
							<option value={it.script}>{it.name} ({it.script})</option>
						{/each}
					</select>
				</label>

				<div class="form-row">
					<label class="form-field">
						<span class="field-label">Имя новой службы <span class="required">*</span>:</span>
						<input
							type="text"
							placeholder="например: my-service-2"
							bind:value={cloneTargetName}
						/>
					</label>

					<label class="form-field" style="max-width: 140px;">
						<span class="field-label">Приоритет:</span>
						<input
							type="number"
							min="10"
							max="99"
							bind:value={clonePriority}
						/>
					</label>
				</div>
			</div>
		{:else}
			<!-- Custom Mode -->
			<div class="form-grid">
				<label class="form-field">
					<span class="field-label">Имя файла в /opt/etc/init.d/ <span class="required">*</span>:</span>
					<input
						type="text"
						placeholder="S90custom-daemon"
						bind:value={customScriptName}
					/>
				</label>

				<label class="form-field">
					<span class="field-label">Код init-скрипта:</span>
					<textarea
						rows="12"
						class="code-textarea"
						bind:value={customScriptContent}
					></textarea>
				</label>
			</div>
		{/if}
	</div>

	{#snippet actions()}
		<div class="modal-footer-btns">
			<Button variant="ghost" onclick={() => (showCreateModal = false)}>Отмена</Button>
			<Button variant="primary" loading={creating} onclick={handleCreateService}>
				{#snippet iconBefore()}<Check size={14} />{/snippet}
				Создать и активировать службу
			</Button>
		</div>
	{/snippet}
</Modal>

<!-- Modal: Edit Script -->
<Modal
	open={editingItem !== null}
	title={`Редактирование скрипта службы: ${editingItem?.name || ''}`}
	size="lg"
	onclose={() => (editingItem = null)}
>
	{#if editingItem}
		<div class="edit-modal-root">
			<div class="edit-meta-bar">
				<code>{editingItem.script}</code>
				<span class="edit-hint">Права доступа 0755 (rwxr-xr-x) сохраняются автоматически</span>
			</div>

			{#if editLoading}
				<div class="empty-state">
					<RefreshCw size={24} class="spin" />
					<p>Загрузка содержимого скрипта…</p>
				</div>
			{:else}
				<textarea
					rows="16"
					class="code-textarea"
					bind:value={editContent}
				></textarea>
			{/if}
		</div>
	{/if}

	{#snippet actions()}
		<div class="modal-footer-btns">
			<Button variant="ghost" onclick={() => (editingItem = null)}>Отмена</Button>
			<Button variant="secondary" loading={editSaving} onclick={() => handleSaveEdit(true)}>
				{#snippet iconBefore()}<RotateCw size={13} />{/snippet}
				Сохранить и перезапустить
			</Button>
			<Button variant="primary" loading={editSaving} onclick={() => handleSaveEdit(false)}>
				{#snippet iconBefore()}<Check size={13} />{/snippet}
				Сохранить
			</Button>
		</div>
	{/snippet}
</Modal>

<!-- Modal: Delete Confirmation -->
<Modal
	open={deletingItem !== null}
	title="Удаление службы"
	size="sm"
	onclose={() => (deletingItem = null)}
>
	{#if deletingItem}
		<div class="delete-modal-content">
			<AlertTriangle size={28} class="danger-icon" />
			<div>
				<p>Вы действительно хотите удалить службу <strong>{deletingItem.name}</strong>?</p>
				<p class="muted-p">Служба будет остановлена, а скрипт <code>{deletingItem.script}</code> безвозвратно удален с роутера.</p>
			</div>
		</div>
	{/if}

	{#snippet actions()}
		<div class="modal-footer-btns">
			<Button variant="ghost" onclick={() => (deletingItem = null)}>Отмена</Button>
			<Button variant="danger" loading={deleting} onclick={handleDeleteService}>
				{#snippet iconBefore()}<Trash2 size={13} />{/snippet}
				Удалить службу
			</Button>
		</div>
	{/snippet}
</Modal>

{#if pendingAction}
	<ConfirmModal
		open={!!pendingAction}
		title="Остановить управляемую службу?"
		message={pendingAction.item.managedHint || pendingAction.item.name}
		confirmLabel="Остановить"
		variant="danger"
		busy={acting === pendingAction.item.script}
		onClose={() => (pendingAction = null)}
		onConfirm={() => {
			if (pendingAction) void runAction(pendingAction.item, pendingAction.action);
		}}
	/>
{/if}

<style>
	.services-root {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}

	.head-toolbar {
		display: flex;
		justify-content: space-between;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.65rem;
	}

	.toolbar-left, .toolbar-right {
		display: flex;
		align-items: center;
		gap: 0.6rem;
		flex-wrap: wrap;
	}

	.panel-title-wrap {
		display: flex;
		align-items: center;
		gap: 0.45rem;
	}
	.panel-title-wrap h3 {
		margin: 0;
		font-size: 0.95rem;
		font-weight: 700;
	}

	.count-badge {
		font-size: 0.72rem;
		font-weight: 700;
		padding: 0.08rem 0.45rem;
		border-radius: 999px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-muted);
	}

	.search-box {
		position: relative;
		display: flex;
		align-items: center;
	}

	:global(.search-icon) {
		position: absolute;
		left: 0.55rem;
		color: var(--color-text-muted);
	}

	.search-box input {
		padding: 0.3rem 0.55rem 0.3rem 1.65rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		font-size: 0.8rem;
		width: 220px;
	}

	.search-box input:focus {
		border-color: var(--color-accent);
		outline: none;
	}

	/* Table */
	.table-wrap {
		overflow-x: auto;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
	}

	.svc-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.82rem;
	}

	.svc-table th, .svc-table td {
		padding: 0.55rem 0.75rem;
		border-bottom: 1px solid var(--color-border);
		text-align: left;
		vertical-align: middle;
	}

	.svc-table th {
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
		font-size: 0.72rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.03em;
	}

	.svc-table tr:hover {
		background: var(--color-bg-hover, rgba(255, 255, 255, 0.03));
	}

	.svc-table tr.is-managed {
		background: var(--color-accent-tint, rgba(96, 165, 250, 0.04));
	}

	.name-cell-wrap {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
	}

	.name-line {
		display: flex;
		align-items: center;
		gap: 0.4rem;
	}

	.svc-name {
		font-weight: 700;
		color: var(--color-text-primary);
		font-size: 0.88rem;
	}

	.badge-managed {
		font-size: 0.65rem;
		font-weight: 700;
		padding: 0.05rem 0.3rem;
		border-radius: 3px;
		background: rgba(96, 165, 250, 0.18);
		color: #60a5fa;
	}

	.svc-path {
		font-size: 0.73rem;
		color: var(--color-text-muted);
		font-family: var(--font-mono, monospace);
	}

	.status-cell-wrap {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
	}

	.status-pill {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		font-size: 0.75rem;
		font-weight: 600;
		padding: 0.12rem 0.5rem;
		border-radius: 999px;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		color: var(--color-text-muted);
		width: fit-content;
	}
	.status-pill .dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: #94a3b8;
	}
	.status-pill.running {
		background: rgba(16, 185, 129, 0.12);
		border-color: rgba(16, 185, 129, 0.35);
		color: #059669;
	}
	:global(.dark) .status-pill.running {
		background: rgba(16, 185, 129, 0.15);
		border-color: rgba(16, 185, 129, 0.35);
		color: #34d399;
	}
	.status-pill.running .dot {
		background: #10b981;
		box-shadow: 0 0 6px rgba(16, 185, 129, 0.6);
	}

	.status-hint-text {
		font-size: 0.72rem;
		color: var(--color-text-muted);
		max-width: 240px;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.action-buttons-group {
		display: flex;
		align-items: center;
		justify-content: flex-end;
		gap: 0.3rem;
		flex-wrap: wrap;
	}

	.btn-act {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		padding: 0.25rem 0.45rem;
		font-size: 0.75rem;
		font-weight: 600;
		border-radius: var(--radius-sm, 5px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-tertiary);
		color: var(--color-text-secondary);
		cursor: pointer;
		transition: all 0.15s ease;
	}
	.btn-act:hover:not(:disabled) {
		background: var(--color-bg-hover, rgba(255,255,255,0.08));
		color: var(--color-text-primary);
	}
	.btn-act:disabled {
		opacity: 0.4;
		cursor: not-allowed;
	}

	.btn-start:hover:not(:disabled) {
		background: rgba(16, 185, 129, 0.15);
		color: #10b981;
		border-color: #10b981;
	}
	.btn-stop:hover:not(:disabled) {
		background: rgba(245, 158, 11, 0.15);
		color: #f59e0b;
		border-color: #f59e0b;
	}
	.btn-delete:hover:not(:disabled) {
		background: rgba(239, 68, 68, 0.15);
		color: #ef4444;
		border-color: #ef4444;
	}

	/* Modal tabs */
	.create-modal-root, .edit-modal-root {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.modal-mode-tabs {
		display: flex;
		gap: 0.4rem;
		border-bottom: 1px solid var(--color-border);
		padding-bottom: 0.5rem;
	}

	.mode-tab-btn {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		padding: 0.35rem 0.65rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid transparent;
		background: none;
		color: var(--color-text-muted);
		font-size: 0.8rem;
		font-weight: 600;
		cursor: pointer;
	}
	.mode-tab-btn:hover {
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
	}
	.mode-tab-btn.active {
		background: var(--color-accent-tint, rgba(96, 165, 250, 0.15));
		border-color: var(--color-accent);
		color: var(--color-accent);
	}

	.form-grid {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}

	.form-row {
		display: flex;
		gap: 0.75rem;
	}

	.form-field {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		flex: 1;
	}

	.field-label {
		font-size: 0.78rem;
		font-weight: 600;
		color: var(--color-text-secondary);
	}
	.required {
		color: var(--color-error, #f87171);
	}

	.form-field input, .form-field select {
		padding: 0.4rem 0.55rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		font-size: 0.82rem;
	}
	.form-field input:focus, .form-field select:focus {
		border-color: var(--color-accent);
		outline: none;
	}

	.field-hint {
		font-size: 0.72rem;
		color: var(--color-text-muted);
	}
	.field-hint code {
		color: var(--color-accent);
	}

	.preview-box {
		display: flex;
		flex-direction: column;
		gap: 0.3rem;
		margin-top: 0.25rem;
	}
	.preview-label {
		font-size: 0.74rem;
		font-weight: 600;
		color: var(--color-text-muted);
	}
	.code-preview {
		margin: 0;
		padding: 0.6rem;
		border-radius: var(--radius-sm, 6px);
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		font-size: 0.76rem;
		font-family: var(--font-mono, monospace);
		color: var(--color-text-primary);
		max-height: 180px;
		overflow-y: auto;
	}

	.code-textarea {
		width: 100%;
		box-sizing: border-box;
		padding: 0.6rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
		font-family: var(--font-mono, monospace);
		font-size: 0.8rem;
		line-height: 1.4;
		resize: vertical;
	}
	.code-textarea:focus {
		border-color: var(--color-accent);
		outline: none;
	}

	.edit-meta-bar {
		display: flex;
		justify-content: space-between;
		align-items: center;
		font-size: 0.78rem;
		background: var(--color-bg-secondary);
		padding: 0.35rem 0.6rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
	}
	.edit-hint {
		color: var(--color-text-muted);
		font-size: 0.72rem;
	}

	.delete-modal-content {
		display: flex;
		gap: 0.75rem;
		align-items: flex-start;
		padding: 0.5rem 0;
	}
	:global(.danger-icon) {
		color: var(--color-error, #f87171);
		flex-shrink: 0;
	}
	.muted-p {
		color: var(--color-text-muted);
		font-size: 0.8rem;
		margin: 0.35rem 0 0 0;
	}

	.empty-state {
		padding: 3rem;
		text-align: center;
		color: var(--color-text-muted);
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.5rem;
	}

	.modal-footer-btns {
		display: flex;
		justify-content: flex-end;
		gap: 0.5rem;
		width: 100%;
	}
</style>
