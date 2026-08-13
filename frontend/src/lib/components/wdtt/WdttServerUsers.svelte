<script lang="ts">
	import { untrack } from 'svelte';
	import { Button, Input } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { pluralize } from '$lib/utils/pluralize';
	import type {
		WdttPanelUserEntry,
		WdttPanelUsersStatus,
		WdttServerDeviceEntry,
		WdttServerDevicesStatus
	} from '$lib/types';

	const FETCH_TIMEOUT_MS = 20_000;

	type DistributionTab = 'passwords' | 'wg' | 'raw';

	interface Props {
		serverInstanceId: string;
		serverMainPassword?: string;
		canManage?: boolean;
		onGenerateForUser?: (user: WdttPanelUserEntry) => void;
	}

	let {
		serverInstanceId,
		serverMainPassword = '',
		canManage = true,
		onGenerateForUser
	}: Props = $props();

	let activeTab = $state<DistributionTab>('passwords');
	let loading = $state(false);
	let adding = $state(false);
	let removing = $state('');
	let loadError = $state('');
	let status = $state<WdttPanelUsersStatus | null>(null);

	let devicesLoading = $state(false);
	let devicesError = $state('');
	let wgDevices = $state<WdttServerDevicesStatus | null>(null);
	let rawDevices = $state<WdttServerDevicesStatus | null>(null);
	let deviceAction = $state('');
	let editingDeviceId = $state('');
	let editIP = $state('');

	let newComment = $state('');
	let newVkHash = $state('');
	let newPassword = $state('');

	let newDeviceId = $state('');
	let newDeviceIP = $state('');
	let newDeviceComment = $state('');

	let reloadSeq = 0;
	let loadedForId = $state('');
	let devicesPollTimer: ReturnType<typeof setInterval> | null = null;

	const DEVICES_POLL_MS = 5000;

	function withTimeout<T>(promise: Promise<T>, ms: number): Promise<T> {
		return new Promise((resolve, reject) => {
			const timer = setTimeout(() => reject(new Error('таймаут запроса')), ms);
			promise.then(
				(v) => {
					clearTimeout(timer);
					resolve(v);
				},
				(e) => {
					clearTimeout(timer);
					reject(e);
				}
			);
		});
	}

	async function reloadPasswords() {
		const seq = ++reloadSeq;
		loading = true;
		loadError = '';
		try {
			const result = await withTimeout(
				api.getWdttServerPanelUsers(serverInstanceId),
				FETCH_TIMEOUT_MS
			);
			if (seq !== reloadSeq) return;
			status = result;
		} catch (e) {
			if (seq !== reloadSeq) return;
			loadError = e instanceof Error ? e.message : String(e);
			notifications.error('Не удалось загрузить пароли: ' + loadError);
		} finally {
			if (seq === reloadSeq) loading = false;
		}
	}

	async function reloadDevices(mode: 'wg' | 'raw', silent = false) {
		if (!silent) {
			devicesLoading = true;
		}
		devicesError = '';
		try {
			const result = await withTimeout(
				api.getWdttServerDevices(serverInstanceId, mode),
				FETCH_TIMEOUT_MS
			);
			if (mode === 'wg') wgDevices = result;
			else rawDevices = result;
		} catch (e) {
			devicesError = e instanceof Error ? e.message : String(e);
			if (!silent) {
				notifications.error('Не удалось загрузить устройства: ' + devicesError);
			}
		} finally {
			if (!silent) devicesLoading = false;
		}
	}

	async function reloadAll() {
		await reloadPasswords();
		if (activeTab === 'wg') await reloadDevices('wg');
		if (activeTab === 'raw') await reloadDevices('raw');
	}

	$effect(() => {
		const id = serverInstanceId;
		untrack(() => {
			if (id === loadedForId) return;
			loadedForId = id;
			void reloadPasswords();
		});
	});

	$effect(() => {
		const tab = activeTab;
		const id = serverInstanceId;
		untrack(() => {
			if (devicesPollTimer) {
				clearInterval(devicesPollTimer);
				devicesPollTimer = null;
			}
			if (tab === 'wg' || tab === 'raw') {
				void reloadDevices('wg');
				void reloadDevices('raw');
				devicesPollTimer = setInterval(() => {
					void reloadDevices('wg', true);
					void reloadDevices('raw', true);
				}, DEVICES_POLL_MS);
			}
			void id;
		});
		return () => {
			if (devicesPollTimer) {
				clearInterval(devicesPollTimer);
				devicesPollTimer = null;
			}
		};
	});

	async function addUser() {
		if (adding || !newComment.trim()) return;
		if (!serverMainPassword.trim()) {
			notifications.error('Сначала задайте пароль сервера на вкладке «Основное»');
			return;
		}
		adding = true;
		try {
			const hadCustomPass = !!newPassword.trim();
			status = await withTimeout(
				api.addWdttServerPanelUser(serverInstanceId, {
					comment: newComment.trim(),
					vkHash: newVkHash.trim(),
					password: newPassword.trim() || undefined,
					mainPassword: serverMainPassword.trim()
				}),
				FETCH_TIMEOUT_MS
			);
			newComment = '';
			newVkHash = '';
			newPassword = '';
			loadError = '';
			notifications.success(
				hadCustomPass
					? 'Клиент добавлен — используйте указанный пароль в ссылке wdtt://'
					: 'Клиент добавлен — пароль сгенерирован автоматически'
			);
		} catch (e) {
			notifications.error(
				'Не удалось добавить клиента: ' + (e instanceof Error ? e.message : String(e))
			);
		} finally {
			adding = false;
		}
	}

	async function removeUser(password: string) {
		removing = password;
		try {
			status = await withTimeout(
				api.removeWdttServerPanelUser(serverInstanceId, password),
				FETCH_TIMEOUT_MS
			);
			notifications.success('Клиент удалён');
		} catch (e) {
			notifications.error(
				'Не удалось удалить клиента: ' + (e instanceof Error ? e.message : String(e))
			);
		} finally {
			removing = '';
		}
	}

	async function addDevice(mode: 'wg' | 'raw') {
		if (!newDeviceId.trim()) {
			notifications.error('Укажите device id');
			return;
		}
		deviceAction = 'add';
		try {
			const result = await api.addWdttServerDevice(serverInstanceId, {
				deviceId: newDeviceId.trim(),
				mode,
				ip: mode === 'wg' ? newDeviceIP.trim() || undefined : undefined,
				rawIp: mode === 'raw' ? newDeviceIP.trim() || undefined : undefined,
				comment: newDeviceComment.trim() || undefined
			});
			if (mode === 'wg') wgDevices = result;
			else rawDevices = result;
			newDeviceId = '';
			newDeviceIP = '';
			newDeviceComment = '';
			notifications.success('Устройство добавлено в passwords.json');
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : String(e));
		} finally {
			deviceAction = '';
		}
	}

	function startEditDevice(entry: WdttServerDeviceEntry, mode: 'wg' | 'raw') {
		editingDeviceId = entry.deviceId;
		editIP = mode === 'wg' ? (entry.ip ?? '') : (entry.rawIp ?? '');
	}

	function cancelEditDevice() {
		editingDeviceId = '';
		editIP = '';
	}

	async function saveEditDevice(mode: 'wg' | 'raw') {
		if (!editingDeviceId) return;
		deviceAction = editingDeviceId;
		try {
			const result = await api.updateWdttServerDevice(serverInstanceId, editingDeviceId, {
				mode,
				ip: mode === 'wg' ? editIP.trim() || undefined : undefined,
				rawIp: mode === 'raw' ? editIP.trim() || undefined : undefined
			});
			if (mode === 'wg') wgDevices = result;
			else rawDevices = result;
			cancelEditDevice();
			notifications.success('IP обновлён — клиенту нужен reconnect');
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : String(e));
		} finally {
			deviceAction = '';
		}
	}

	async function unbindDevice(deviceId: string, mode: 'wg' | 'raw') {
		deviceAction = 'unbind:' + deviceId;
		try {
			const result = await api.unbindWdttServerDevice(serverInstanceId, deviceId, mode);
			if (mode === 'wg') wgDevices = result;
			else rawDevices = result;
			notifications.success('Привязка к паролю снята');
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : String(e));
		} finally {
			deviceAction = '';
		}
	}

	async function removeDevice(deviceId: string, mode: 'wg' | 'raw') {
		deviceAction = 'del:' + deviceId;
		try {
			const result = await api.removeWdttServerDevice(serverInstanceId, deviceId, mode);
			if (mode === 'wg') wgDevices = result;
			else rawDevices = result;
			notifications.success('Устройство удалено');
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : String(e));
		} finally {
			deviceAction = '';
		}
	}

	async function copyText(text: string, label: string) {
		try {
			await navigator.clipboard.writeText(text);
			notifications.success(`${label} скопирован`);
		} catch {
			notifications.error('Не удалось скопировать');
		}
	}

	function shortPass(pass: string): string {
		if (pass.length <= 16) return pass;
		return `${pass.slice(0, 8)}…${pass.slice(-6)}`;
	}

	function formatBytes(n?: number): string {
		if (!n) return '—';
		if (n < 1024) return `${n} B`;
		if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
		return `${(n / (1024 * 1024)).toFixed(1)} MB`;
	}

	function deviceIP(entry: WdttServerDeviceEntry, mode: 'wg' | 'raw'): string {
		return mode === 'wg' ? (entry.ip ?? '') : (entry.rawIp ?? '');
	}

	function sortDevices(list: WdttServerDeviceEntry[]): WdttServerDeviceEntry[] {
		return [...list].sort((a, b) => {
			const aOn = a.active ? 1 : 0;
			const bOn = b.active ? 1 : 0;
			if (aOn !== bOn) return bOn - aOn;
			return a.deviceId.localeCompare(b.deviceId);
		});
	}

	const users = $derived(status?.users ?? []);
	const extraUsers = $derived(users.filter((u) => !u.isMain));
	const panelMissing = $derived(!!status && !status.available);
	const deviceMode = $derived(activeTab === 'wg' ? 'wg' : 'raw');
	const devicesStatus = $derived(activeTab === 'wg' ? wgDevices : rawDevices);
	const currentDevices = $derived(sortDevices(devicesStatus?.devices ?? []));
	const onlineCount = $derived(devicesStatus?.activeDeviceCount ?? 0);
	const serverRunning = $derived(devicesStatus?.serverRunning ?? false);
	const statsActive = $derived(devicesStatus?.statsActive ?? 0);
</script>

<div class="wdtt-users">
	<div class="wdtt-users-head">
		<div>
			<div class="section-label">Раздача</div>
			<p class="wdtt-hint">
				Пароли — wdtt.json. Устройства WireGuard и Raw — passwords.json (появляются после
				подключения клиента или резервируются заранее).
			</p>
		</div>
		{#if !loading && !devicesLoading}
			<Button variant="ghost" size="sm" onclick={reloadAll}>Обновить</Button>
		{/if}
	</div>

	<div class="wdtt-tabs" role="tablist">
		<button
			type="button"
			class="wdtt-tab"
			class:wdtt-tab-active={activeTab === 'passwords'}
			role="tab"
			aria-selected={activeTab === 'passwords'}
			onclick={() => (activeTab = 'passwords')}
		>
			Пароли
		</button>
		<button
			type="button"
			class="wdtt-tab"
			class:wdtt-tab-active={activeTab === 'wg'}
			role="tab"
			aria-selected={activeTab === 'wg'}
			onclick={() => (activeTab = 'wg')}
		>
			WireGuard
			{#if wgDevices?.activeDeviceCount}
				<span class="wdtt-tab-badge wdtt-tab-badge-online">{wgDevices.activeDeviceCount}</span>
			{/if}
		</button>
		<button
			type="button"
			class="wdtt-tab"
			class:wdtt-tab-active={activeTab === 'raw'}
			role="tab"
			aria-selected={activeTab === 'raw'}
			onclick={() => (activeTab = 'raw')}
		>
			Raw
			{#if rawDevices?.activeDeviceCount}
				<span class="wdtt-tab-badge wdtt-tab-badge-online">{rawDevices.activeDeviceCount}</span>
			{/if}
		</button>
	</div>

	{#if activeTab === 'passwords'}
		{#if loading && !users.length && !loadError}
			<p class="wdtt-hint">Загрузка…</p>
		{:else if loadError && !users.length}
			<p class="wdtt-empty wdtt-empty-error">
				{loadError}
				<Button variant="secondary" size="sm" onclick={reloadPasswords}>Повторить</Button>
			</p>
		{:else if users.length === 0}
			<p class="wdtt-empty">
				Задайте пароль сервера на вкладке «Основное» — он станет основным клиентом.
			</p>
		{:else}
			{#if panelMissing}
				<p class="wdtt-empty wdtt-empty-warn">
					panel.db недоступна — список паролей из wdtt.json. Устройства смотрите во вкладках
					WireGuard / Raw (passwords.json).
				</p>
			{/if}
			<ul class="wdtt-users-list">
				{#each users as entry (entry.password)}
					<li class="wdtt-users-item">
						<div class="wdtt-users-main">
							<span class="wdtt-users-name">{entry.comment || '—'}</span>
							<code class="wdtt-users-pass" title={entry.password}>{shortPass(entry.password)}</code>
							{#if entry.isMain}
								<span class="wdtt-users-badge wdtt-users-badge-main">основной</span>
							{/if}
							{#if entry.isDeactivated}
								<span class="wdtt-users-badge">отключён</span>
							{/if}
						</div>
						<div class="wdtt-users-actions">
							{#if onGenerateForUser}
								<Button variant="ghost" size="sm" onclick={() => onGenerateForUser?.(entry)}>
									Ссылка
								</Button>
							{/if}
							{#if !entry.isMain}
								<Button
									variant="ghost"
									size="sm"
									loading={removing === entry.password}
									disabled={!canManage}
									onclick={() => removeUser(entry.password)}
								>
									Удалить
								</Button>
							{/if}
						</div>
					</li>
				{/each}
			</ul>
			<p class="wdtt-hint wdtt-users-foot">
				{#if extraUsers.length}
					{pluralize(extraUsers.length, ['клиент', 'клиента', 'клиентов'])} с отдельным паролем
				{:else}
					Отдельных паролей нет — основной подходит всем
				{/if}
			</p>
		{/if}

		{#if canManage}
			<div class="wdtt-users-add">
				<Input label="Имя / комментарий" bind:value={newComment} placeholder="Клиент Иван" />
				<Input
					label="Пароль (необязательно)"
					bind:value={newPassword}
					placeholder="авто — если пусто"
					type="password"
				/>
				<Input
					label="VK-хеш (необязательно)"
					bind:value={newVkHash}
					placeholder="vk.com/call/join/…"
				/>
				<Button
					variant="secondary"
					size="sm"
					loading={adding}
					disabled={!newComment.trim() || adding}
					onclick={addUser}
				>
					Добавить клиента
				</Button>
			</div>
		{/if}
	{:else}
		{@const mode = deviceMode}
		{@const jsonPath = devicesStatus?.passwordsJsonPath}

		{#if serverRunning && statsActive > 0 && onlineCount === 0}
			<p class="wdtt-hint wdtt-dev-online-hint">
				На сервере активных сессий: {statsActive} — ждём обновления stats (до 10 с) или перезапустите wdtt-server
			</p>
		{/if}

		{#if devicesLoading && !currentDevices.length}
			<p class="wdtt-hint">Загрузка устройств…</p>
		{:else if devicesError && !currentDevices.length}
			<p class="wdtt-empty wdtt-empty-error">
				{devicesError}
				<Button variant="secondary" size="sm" onclick={() => reloadDevices(mode)}>Повторить</Button>
			</p>
		{:else if currentDevices.length === 0}
			<p class="wdtt-empty">
				{#if mode === 'raw'}
					Пока нет raw-устройств. Появятся после первого GETCONF_RAW или добавьте резерв ниже
					(device id с клиента, например awgm-…).
				{:else}
					Пока нет WG-устройств. Появятся после первого GETCONF или зарезервируйте слот заранее.
				{/if}
			</p>
		{:else}
			<div class="wdtt-dev-table-wrap">
				<table class="wdtt-dev-table">
					<thead>
						<tr>
							<th class="wdtt-dev-col-status" aria-label="Статус"></th>
							<th>Device ID</th>
							<th>{mode === 'wg' ? 'IP (10.66.x)' : 'Raw IP'}</th>
							<th>Пароль / комментарий</th>
							<th>Трафик</th>
							<th></th>
						</tr>
					</thead>
					<tbody>
						{#each currentDevices as entry (entry.deviceId)}
							<tr class:wdtt-dev-row-active={entry.active}>
								<td class="wdtt-dev-col-status">
									<span
										class="wdtt-dev-dot"
										class:wdtt-dev-dot-on={entry.active}
										title={entry.active ? 'Подключён' : entry.activeKnown ? 'Не подключён' : '—'}
									></span>
								</td>
								<td>
									<code class="wdtt-dev-id" title={entry.deviceId}>{entry.deviceId}</code>
									{#if entry.reserved}
										<span class="wdtt-users-badge">резерв</span>
									{/if}
								</td>
								<td>
									{#if editingDeviceId === entry.deviceId}
										<Input bind:value={editIP} placeholder={mode === 'wg' ? '10.66.0.3' : '10.70.0.3'} />
									{:else}
										<code>{deviceIP(entry, mode) || '—'}</code>
									{/if}
								</td>
								<td>{entry.passwordComment || entry.comment || '—'}</td>
								<td class="wdtt-dev-traffic">
									↓ {formatBytes(entry.downBytes)} · ↑ {formatBytes(entry.upBytes)}
								</td>
								<td class="wdtt-dev-actions">
									{#if canManage}
										{#if editingDeviceId === entry.deviceId}
											<Button
												variant="ghost"
												size="sm"
												loading={deviceAction === entry.deviceId}
												onclick={() => saveEditDevice(mode)}
											>
												Сохранить
											</Button>
											<Button variant="ghost" size="sm" onclick={cancelEditDevice}>Отмена</Button>
										{:else}
											<Button variant="ghost" size="sm" onclick={() => copyText(entry.deviceId, 'Device ID')}>
												ID
											</Button>
											<Button variant="ghost" size="sm" onclick={() => startEditDevice(entry, mode)}>IP</Button>
											<Button
												variant="ghost"
												size="sm"
												loading={deviceAction === 'unbind:' + entry.deviceId}
												onclick={() => unbindDevice(entry.deviceId, mode)}
											>
												Отвязать
											</Button>
											<Button
												variant="ghost"
												size="sm"
												loading={deviceAction === 'del:' + entry.deviceId}
												onclick={() => removeDevice(entry.deviceId, mode)}
											>
												Удалить
											</Button>
										{/if}
									{/if}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}

		{#if jsonPath}
			<p class="wdtt-hint wdtt-users-foot"><code>{jsonPath}</code></p>
		{/if}

		{#if canManage}
			<div class="wdtt-users-add">
				<Input
					label="Device ID"
					bind:value={newDeviceId}
					placeholder="awgm-0e1e9526… (с клиента)"
				/>
				<Input
					label={mode === 'wg' ? 'IP (необязательно)' : 'Raw IP (необязательно)'}
					bind:value={newDeviceIP}
					placeholder={mode === 'wg' ? '10.66.0.10' : '10.70.66.10'}
				/>
				<Input label="Комментарий" bind:value={newDeviceComment} placeholder="Дом" />
				<Button
					variant="secondary"
					size="sm"
					loading={deviceAction === 'add'}
					disabled={!newDeviceId.trim()}
					onclick={() => addDevice(mode)}
				>
					Зарезервировать
				</Button>
			</div>
		{/if}
	{/if}
</div>

<style>
	.wdtt-users {
		margin-top: 1rem;
		padding-top: 1rem;
		border-top: 1px solid var(--color-border);
	}

	.wdtt-users-head {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 0.75rem;
		margin-bottom: 0.625rem;
	}

	.wdtt-tabs {
		display: flex;
		gap: 0.25rem;
		margin-bottom: 0.75rem;
		border-bottom: 1px solid var(--color-border);
	}

	.wdtt-tab {
		appearance: none;
		background: none;
		border: none;
		border-bottom: 2px solid transparent;
		padding: 0.375rem 0.75rem;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		cursor: pointer;
		margin-bottom: -1px;
	}

	.wdtt-tab-active {
		color: var(--color-primary, #2563eb);
		border-bottom-color: var(--color-primary, #2563eb);
		font-weight: 500;
	}

	.wdtt-tab-badge {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		min-width: 1.125rem;
		height: 1.125rem;
		margin-left: 0.375rem;
		padding: 0 0.25rem;
		border-radius: 999px;
		font-size: 0.625rem;
		font-weight: 600;
		line-height: 1;
		vertical-align: middle;
	}

	.wdtt-tab-badge-online {
		background: rgba(34, 197, 94, 0.15);
		color: #16a34a;
	}

	.wdtt-hint {
		font-size: 0.75rem;
		color: var(--color-text-secondary);
		margin: 0.25rem 0 0;
	}

	.wdtt-empty {
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		margin: 0;
		padding: 0.625rem 0.75rem;
		border: 1px dashed var(--color-border);
		border-radius: var(--radius-sm);
	}

	.wdtt-empty-error {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem;
	}

	.wdtt-empty-warn {
		margin-bottom: 0.5rem;
		border-style: solid;
		border-color: var(--color-warning, #b45309);
		color: var(--color-warning, #b45309);
	}

	.wdtt-users-list {
		list-style: none;
		margin: 0;
		padding: 0;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		overflow: hidden;
	}

	.wdtt-users-item {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		padding: 0.5rem 0.75rem;
		background: var(--color-bg-primary);
		border-bottom: 1px solid var(--color-border);
	}

	.wdtt-users-item:last-child {
		border-bottom: none;
	}

	.wdtt-users-main {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.375rem 0.625rem;
		min-width: 0;
	}

	.wdtt-users-name {
		font-size: 0.875rem;
		font-weight: 500;
	}

	.wdtt-users-pass {
		font-family: var(--font-mono);
		font-size: 0.75rem;
		color: var(--color-text-secondary);
	}

	.wdtt-users-badge {
		font-size: 0.6875rem;
		color: var(--color-text-secondary);
		padding: 0.125rem 0.375rem;
		border-radius: 4px;
		background: var(--color-surface-secondary, rgba(0, 0, 0, 0.04));
	}

	.wdtt-users-badge-main {
		color: var(--color-primary, #2563eb);
	}

	.wdtt-users-badge-online {
		color: #16a34a;
		background: rgba(34, 197, 94, 0.12);
	}

	.wdtt-users-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.25rem;
	}

	.wdtt-users-foot {
		margin-top: 0.375rem;
	}

	.wdtt-users-add {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
		gap: 0.5rem;
		align-items: end;
		margin-top: 0.75rem;
	}

	.wdtt-dev-id {
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		word-break: break-all;
	}

	.wdtt-dev-online-hint {
		margin-bottom: 0.5rem;
	}

	.wdtt-dev-table-wrap {
		overflow-x: auto;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
	}

	.wdtt-dev-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.8125rem;
	}

	.wdtt-dev-table th,
	.wdtt-dev-table td {
		padding: 0.5rem 0.625rem;
		text-align: left;
		border-bottom: 1px solid var(--color-border);
		vertical-align: middle;
	}

	.wdtt-dev-table th {
		font-size: 0.6875rem;
		text-transform: uppercase;
		letter-spacing: 0.03em;
		color: var(--color-text-secondary);
		background: var(--color-surface-secondary, rgba(0, 0, 0, 0.03));
	}

	.wdtt-dev-table tr:last-child td {
		border-bottom: none;
	}

	.wdtt-dev-col-status {
		width: 1.25rem;
		padding-right: 0.25rem;
	}

	.wdtt-dev-dot {
		display: inline-block;
		width: 0.5rem;
		height: 0.5rem;
		border-radius: 50%;
		background: var(--color-border);
		opacity: 0.55;
	}

	.wdtt-dev-dot-on {
		background: #22c55e;
		opacity: 1;
		box-shadow: 0 0 0 2px rgba(34, 197, 94, 0.25);
	}

	.wdtt-dev-row-active {
		background: rgba(34, 197, 94, 0.04);
	}

	.wdtt-dev-traffic {
		white-space: nowrap;
		color: var(--color-text-secondary);
		font-size: 0.75rem;
	}

	.wdtt-dev-actions {
		white-space: nowrap;
	}
</style>
