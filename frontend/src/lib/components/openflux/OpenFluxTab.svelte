<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import QRCode from 'qrcode';
	import {
		AlertTriangle,
		Check,
		Copy,
		Download,
		Play,
		QrCode,
		RefreshCw,
		RotateCw,
		Square,
		Terminal,
	} from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { errText } from '$lib/utils/errorMessage';
	import { createSelfReschedulingPoll } from '$lib/utils/selfReschedulingPoll';
	import { Badge, Button, Card, Dropdown, Input, StatusDot, Toggle } from '$lib/components/ui';
	import { LoadingSpinner } from '$lib/components/layout';
	import QrZoomImage from '$lib/components/ui/QrZoomImage.svelte';
	import SensitiveInput from '../proxy-panel/SensitiveInput.svelte';
	import type { OpenFluxClientConfig, OpenFluxConfig, OpenFluxStatus } from '$lib/types';

	const TRANSPORTS = [
		{ id: 'webdav', label: 'WebDAV' },
		{ id: 'mailru', label: 'Mail.ru Документы' },
		{ id: 'yandex', label: 'Яндекс Диск' },
		{ id: 'vyandex', label: 'Яндекс (просмотр)' },
		{ id: 'boards', label: 'Яндекс Доски' },
		{ id: 'avito', label: 'Avito' },
		{ id: 'cupsonline', label: 'CupsOnline' },
		{ id: 'telemost', label: 'Яндекс Телемост' },
		{ id: 'oneme', label: 'MAX' },
		{ id: 'direct', label: 'Direct TCP' },
	] as const;

	function defaultServerConfig(): OpenFluxConfig {
		return {
			enabled: false,
			mode: 'l4',
			transportMode: 'session',
			transports: ['mailru', 'yandex', 'vyandex'],
			webdavUrl: '',
			webdavUser: '',
			webdavPassword: '',
			mailruUrl: '',
			yandexUrl: '',
			vyandexUrl: '',
			boardsUrl: '',
			avitoUrl: '',
			avitoCookies: '',
			cupsonlineUrl: '',
			telemostUrl: '',
			onemeToken: '',
			onemeUid: '',
			directListen: '0.0.0.0:8445',
			directHost: '',
			secret: '',
			codec: 'batched',
			upstreamProxy: '',
			lowMemory: true,
			lanes: 0,
			nativeScriptsDir: '',
			openFirewall: true,
			debug: 0,
			name: 'OpenFlux',
			natMode: 'full',
			natStaticWan: '',
			policy: '',
			exposeToPolicies: true,
			egressInterface: '',
			tunMtu: 1280,
		};
	}

	function normalizeServerConfig(c?: OpenFluxConfig | null): OpenFluxConfig {
		const base = defaultServerConfig();
		const m = { ...base, ...(c ?? {}) };
		return {
			...m,
			mode: m.mode || 'l4',
			transportMode: m.transportMode || 'session',
			transports: Array.isArray(m.transports) && m.transports.length ? [...m.transports] : [...base.transports],
			webdavUrl: m.webdavUrl ?? '',
			webdavUser: m.webdavUser ?? '',
			webdavPassword: m.webdavPassword ?? '',
			mailruUrl: m.mailruUrl ?? '',
			yandexUrl: m.yandexUrl ?? '',
			vyandexUrl: m.vyandexUrl ?? '',
			boardsUrl: m.boardsUrl ?? '',
			avitoUrl: m.avitoUrl ?? '',
			avitoCookies: m.avitoCookies ?? '',
			cupsonlineUrl: m.cupsonlineUrl ?? '',
			telemostUrl: m.telemostUrl ?? '',
			onemeToken: m.onemeToken ?? '',
			onemeUid: m.onemeUid ?? '',
			directListen: m.directListen ?? '0.0.0.0:8445',
			directHost: m.directHost ?? '',
			secret: m.secret ?? '',
			codec: m.codec || 'batched',
			upstreamProxy: m.upstreamProxy ?? '',
			lowMemory: m.lowMemory ?? true,
			lanes: m.lanes ?? 0,
			nativeScriptsDir: m.nativeScriptsDir ?? '',
			name: m.name ?? 'OpenFlux',
			natMode: m.natMode || 'full',
			natStaticWan: m.natStaticWan ?? '',
			policy: m.policy ?? '',
			exposeToPolicies: m.exposeToPolicies ?? true,
			egressInterface: m.egressInterface ?? '',
			tunMtu: m.tunMtu ?? 1280,
		};
	}

	function formatUptimeS(sec?: number): string {
		if (!sec || sec <= 0) return '—';
		const h = Math.floor(sec / 3600);
		const m = Math.floor((sec % 3600) / 60);
		const s = sec % 60;
		if (h > 0) return `${h}ч ${m}м`;
		if (m > 0) return `${m}м ${s}с`;
		return `${s}с`;
	}

	let loading = $state(true);
	let status = $state<OpenFluxStatus | null>(null);
	let draft = $state<OpenFluxConfig>(defaultServerConfig());
	let dirty = $state(false);
	let busy = $state(false);
	let installing = $state(false);
	let saving = $state(false);

	let shareLink = $state('');
	let qrOpen = $state(false);
	let qrUrl = $state('');
	let logs = $state('');
	let logsOpen = $state(false);

	// Client quick control state
	let clientLinkInput = $state('');
	let parsingLink = $state(false);
	let clientLogs = $state('');

	const hasUpdate = $derived(
		Boolean(
			status?.installed &&
				status?.version &&
				status?.latestVersion &&
				status.version !== status.latestVersion,
		),
	);

	async function loadStatus() {
		const res = await api.openfluxStatus();
		status = res;
		if (!dirty) {
			draft = normalizeServerConfig(res.config);
		}
		if (res.link) {
			shareLink = res.link;
		}
		if (logsOpen) {
			try {
				logs = (await api.openfluxGetLogs(120)).logs;
				clientLogs = (await api.openfluxGetClientLogs(120)).logs;
			} catch {
				/* secondary */
			}
		}
	}

	const poll = createSelfReschedulingPoll(async () => {
		try {
			await loadStatus();
		} catch {
			/* ignore polling hiccups */
		}
	});

	onMount(async () => {
		try {
			await loadStatus();
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			loading = false;
		}
		poll.start();
	});

	onDestroy(() => poll.stop());

	function toggleTransport(id: string) {
		dirty = true;
		if (draft.transports.includes(id)) {
			if (draft.transports.length <= 1) {
				notifications.warning('Нужен хотя бы один транспорт');
				return;
			}
			draft.transports = draft.transports.filter((t) => t !== id);
		} else {
			draft.transports = [...draft.transports, id];
		}
	}

	function generateSecret() {
		dirty = true;
		const arr = new Uint8Array(16);
		crypto.getRandomValues(arr);
		draft.secret = Array.from(arr, (b) => b.toString(16).padStart(2, '0')).join('');
	}

	async function installBinary() {
		installing = true;
		try {
			status = await api.openfluxInstall();
			notifications.success('Бинарный файл OpenFlux установлен');
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			installing = false;
		}
	}

	async function saveServer() {
		saving = true;
		try {
			const payload: OpenFluxConfig = {
				...draft,
				upstreamProxy: draft.mode === 'l4' ? (draft.upstreamProxy?.trim() ?? '') : '',
				lanes: Math.max(0, Math.min(16, Number(draft.lanes) || 0)),
			};
			const res = await api.openfluxSaveConfig(payload);
			status = res;
			dirty = false;
			draft = normalizeServerConfig(res.config);
			shareLink = res.link ?? '';
			qrOpen = false;
			qrUrl = '';
			notifications.success('Конфигурация OpenFlux сохранена');
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			saving = false;
		}
	}

	async function serverAction(action: 'start' | 'stop' | 'restart') {
		busy = true;
		try {
			if (action === 'start') status = await api.openfluxStart();
			else if (action === 'stop') status = await api.openfluxStop();
			else status = await api.openfluxRestart();
			draft = normalizeServerConfig(status.config);
			dirty = false;
			shareLink = status.link ?? shareLink;
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			busy = false;
		}
	}

	async function copyShareLink() {
		if (!shareLink) {
			try {
				shareLink = (await api.openfluxGetLink()).link;
			} catch (e) {
				notifications.error(errText(e));
				return;
			}
		}
		if (await copyToClipboard(shareLink)) {
			notifications.success('Ссылка OpenFlux скопирована');
		}
	}

	async function toggleQr() {
		if (qrOpen) {
			qrOpen = false;
			return;
		}
		if (!shareLink) return;
		try {
			qrUrl = await QRCode.toDataURL(shareLink, { width: 220, margin: 1 });
			qrOpen = true;
		} catch (e) {
			notifications.error(errText(e));
		}
	}

	async function toggleLogs() {
		logsOpen = !logsOpen;
		if (logsOpen) {
			try {
				const [sLogs, cLogs] = await Promise.all([
					api.openfluxGetLogs(120),
					api.openfluxGetClientLogs(120),
				]);
				logs = sLogs.logs;
				clientLogs = cLogs.logs;
			} catch (e) {
				notifications.error(errText(e));
			}
		}
	}

	async function applyClientLink() {
		const link = clientLinkInput.trim();
		if (!link) return;
		parsingLink = true;
		try {
			const parsed = await api.openfluxParseLink(link);
			const clientCfg: OpenFluxClientConfig = {
				...parsed.config,
				enabled: true,
				lowMemory: parsed.config.lowMemory ?? true,
			};
			status = await api.openfluxSaveClientConfig(clientCfg);
			clientLinkInput = '';
			notifications.success('Профиль клиента OpenFlux импортирован');
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			parsingLink = false;
		}
	}

	async function clientAction(action: 'start' | 'stop' | 'restart' | 'delete') {
		busy = true;
		try {
			if (action === 'start') status = await api.openfluxClientStart();
			else if (action === 'stop') status = await api.openfluxClientStop();
			else if (action === 'restart') status = await api.openfluxClientRestart();
			else status = await api.openfluxClientDelete();
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			busy = false;
		}
	}
</script>

{#if loading}
	<LoadingSpinner />
{:else if status}
	<div class="of-grid">
		<!-- Binary & Process Bar -->
		<Card>
			<div class="bar-row">
				<div class="bar-left">
					<StatusDot
						variant={status.running ? 'success' : status.error ? 'error' : 'muted'}
						pulse={status.running}
					/>
					<div class="bar-title">
						<strong>OpenFlux Сервер</strong>
						<span class="sub">
							{status.running
								? `Запущен · PID ${status.pid ?? '—'} · ${formatUptimeS(status.uptimeS)}`
								: 'Остановлен'}
						</span>
					</div>
					{#if status.installed}
						<Badge variant="success" size="xs">v{status.version || status.latestVersion || '?'}</Badge>
					{:else}
						<Badge variant="error" size="xs">Бинарь не установлен ({status.arch})</Badge>
					{/if}
					{#if hasUpdate}
						<Badge variant="warning" size="xs">Доступно v{status.latestVersion}</Badge>
					{/if}
				</div>

				<div class="bar-actions">
					{#if !status.installed || hasUpdate}
						<Button
							variant={status.installed ? 'secondary' : 'primary'}
							size="sm"
							loading={installing}
							disabled={!status.archSupported}
							onclick={installBinary}
						>
							{#snippet iconBefore()}<Download size={14} />{/snippet}
							{status.installed ? 'Обновить' : 'Установить бинарь'}
						</Button>
					{/if}
					{#if status.running}
						<Button variant="secondary" size="sm" loading={busy} onclick={() => serverAction('restart')}>
							{#snippet iconBefore()}<RotateCw size={14} />{/snippet}
							Перезапустить
						</Button>
						<Button variant="secondary" size="sm" loading={busy} onclick={() => serverAction('stop')}>
							{#snippet iconBefore()}<Square size={14} />{/snippet}
							Остановить
						</Button>
					{:else}
						<Button
							variant="primary"
							size="sm"
							loading={busy}
							disabled={!status.installed}
							onclick={() => serverAction('start')}
						>
							{#snippet iconBefore()}<Play size={14} />{/snippet}
							Запустить
						</Button>
					{/if}
				</div>
			</div>

			{#if status.error}
				<div class="err-banner">
					<AlertTriangle size={15} />
					<span>{status.error}</span>
				</div>
			{/if}
		</Card>

		<!-- Server Link Box -->
		{#if shareLink}
			<Card>
				<div class="section-head">
					<strong>Ссылка для клиента (openflux://v1/...)</strong>
					<div class="btn-row">
						<Button variant="secondary" size="sm" onclick={copyShareLink}>
							{#snippet iconBefore()}<Copy size={14} />{/snippet}
							Копировать
						</Button>
						<Button variant="ghost" size="sm" onclick={toggleQr}>
							{#snippet iconBefore()}<QrCode size={14} />{/snippet}
							QR
						</Button>
					</div>
				</div>
				<pre class="link-pre">{shareLink}</pre>
				{#if qrOpen && qrUrl}
					<div class="qr-wrap">
						<QrZoomImage src={qrUrl} alt="OpenFlux Share QR" />
					</div>
				{/if}
			</Card>
		{/if}

		<!-- Server Configuration -->
		<Card>
			<div class="section-head">
				<strong>Настройки сервера OpenFlux</strong>
				{#if dirty}
					<Badge variant="warning" size="xs">Есть несохранённые изменения</Badge>
				{/if}
			</div>

			<div class="form-grid" oninput={() => (dirty = true)} onchange={() => (dirty = true)}>
				<Input label="Имя профиля" bind:value={draft.name} fullWidth />
				<Dropdown
					label="Режим ноды"
					bind:value={draft.mode}
					options={[
						{ value: 'l4', label: 'L4 — SOCKS5/TCP-ретранслятор' },
						{ value: 'l3', label: 'L3 — TUN интерфейс и NAT на роутере' },
					]}
					fullWidth
				/>
				<Dropdown
					label="Режим транспорта"
					bind:value={draft.transportMode}
					options={[
						{ value: 'session', label: 'Session (мультитранспорт + автопереключение)' },
						{ value: 'single', label: 'Single (один выбранный транспорт)' },
					]}
					fullWidth
				/>
				<Dropdown
					label="Кодек кадров"
					bind:value={draft.codec}
					options={[
						{ value: 'batched', label: 'batched (рекомендуется)' },
						{ value: 'legacy', label: 'legacy (совместимость)' },
					]}
					fullWidth
				/>
				<div class="secret-cell">
					<SensitiveInput label="Общий ключ (Secret >= 16 симв.)" bind:value={draft.secret} />
					<Button variant="ghost" size="sm" onclick={generateSecret}>
						<RefreshCw size={14} />
						Сгенерировать
					</Button>
				</div>

				{#if draft.mode === 'l4'}
					<Input
						label="Upstream Proxy (--upstream-proxy)"
						bind:value={draft.upstreamProxy}
						placeholder="socks5://127.0.0.1:1080 или пусто"
						hint="Доступно только в режиме L4"
						fullWidth
					/>
				{/if}

				<Input
					label="Параллельные линии (--lanes)"
					type="number"
					min="0"
					max="16"
					value={String(draft.lanes ?? 0)}
					oninput={(v) => {
						dirty = true;
						draft.lanes = Math.max(0, Math.min(16, Number(v) || 0));
					}}
					hint="0 = автовыбор; допустимо 0..16"
					fullWidth
				/>

				<Input
					label="Каталог нативных скриптов (--native-scripts)"
					bind:value={draft.nativeScriptsDir}
					placeholder="/opt/etc/openflux/scripts"
					fullWidth
				/>

				<div class="toggles-row">
					<Toggle
						label="Автозапуск при старте"
						checked={draft.enabled}
						onchange={(v) => {
							dirty = true;
							draft.enabled = v;
						}}
					/>
					<Toggle
						label="Экономия памяти (--low-memory)"
						hint="Оптимально для роутеров Keenetic"
						checked={draft.lowMemory ?? true}
						onchange={(v) => {
							dirty = true;
							draft.lowMemory = v;
						}}
					/>
					<Toggle
						label="Открывать порт в firewall"
						checked={draft.openFirewall}
						onchange={(v) => {
							dirty = true;
							draft.openFirewall = v;
						}}
					/>
				</div>
			</div>

			<div class="subhead">Активные транспорты</div>
			<div class="chips">
				{#each TRANSPORTS as tr (tr.id)}
					<button
						type="button"
						class="chip"
						class:active={draft.transports.includes(tr.id)}
						onclick={() => toggleTransport(tr.id)}
					>
						{#if draft.transports.includes(tr.id)}<Check size={13} />{/if}
						{tr.label}
					</button>
				{/each}
			</div>

			<div class="form-grid" style="margin-top: 0.75rem;" oninput={() => (dirty = true)}>
				{#if draft.transports.includes('webdav')}
					<Input label="WebDAV URL" bind:value={draft.webdavUrl} placeholder="https://webdav.cloud.ru/remote.php/dav/files/user/room" fullWidth />
					<Input label="WebDAV пользователь" bind:value={draft.webdavUser} fullWidth />
					<SensitiveInput label="WebDAV пароль" bind:value={draft.webdavPassword} />
				{/if}
				{#if draft.transports.includes('mailru')}
					<Input label="Mail.ru Документы URL" bind:value={draft.mailruUrl} placeholder="https://cloud.mail.ru/public/..." fullWidth />
				{/if}
				{#if draft.transports.includes('yandex')}
					<Input label="Яндекс Диск (rw) URL" bind:value={draft.yandexUrl} placeholder="https://disk.yandex.ru/edit/disk/..." fullWidth />
				{/if}
				{#if draft.transports.includes('vyandex')}
					<Input label="Яндекс Диск (view) URL" bind:value={draft.vyandexUrl} placeholder="https://disk.yandex.ru/i/..." fullWidth />
				{/if}
				{#if draft.transports.includes('boards')}
					<Input label="Яндекс Доски URL" bind:value={draft.boardsUrl} placeholder="https://boards.yandex.ru/..." fullWidth />
				{/if}
				{#if draft.transports.includes('avito')}
					<Input label="Avito URL / Чат" bind:value={draft.avitoUrl} placeholder="https://www.avito.ru/..." fullWidth />
					<Input label="Avito Cookies" bind:value={draft.avitoCookies} placeholder="sessid=..." fullWidth />
				{/if}
				{#if draft.transports.includes('cupsonline')}
					<Input label="CupsOnline URL" bind:value={draft.cupsonlineUrl} fullWidth />
				{/if}
				{#if draft.transports.includes('telemost')}
					<Input label="Яндекс Телемост URL" bind:value={draft.telemostUrl} placeholder="https://telemost.yandex.ru/j/..." fullWidth />
				{/if}
				{#if draft.transports.includes('oneme')}
					<Input label="MAX Token (--maxToken)" bind:value={draft.onemeToken} fullWidth />
					<Input label="MAX UID (--maxUid)" bind:value={draft.onemeUid} fullWidth />
				{/if}
				{#if draft.transports.includes('direct')}
					<Input label="Direct Listen" bind:value={draft.directListen} placeholder="0.0.0.0:8445" fullWidth />
					<Input label="Direct Public Host (для ссылки)" bind:value={draft.directHost} placeholder="my.domain.ru:8445" fullWidth />
				{/if}
			</div>

			<div class="save-bar">
				<Button variant="primary" loading={saving} onclick={saveServer}>
					Сохранить конфигурацию сервера
				</Button>
				<Button variant="ghost" onclick={toggleLogs}>
					{#snippet iconBefore()}<Terminal size={14} />{/snippet}
					{logsOpen ? 'Скрыть логи' : 'Логи'}
				</Button>
			</div>
		</Card>

		<!-- Quick Client Section -->
		<Card>
			<div class="section-head">
				<div>
					<strong>OpenFlux Клиент (исходящий туннель)</strong>
					<span class="sub" style="margin-left: 0.5rem;">
						{status.client?.running
							? `Запущен · ${status.client.inboundMode === 'socks5' ? status.client.socks5Uri : status.client.ndmsIface || status.client.tunIface || 'TUN'}`
							: 'Остановлен'}
					</span>
				</div>
				<div class="btn-row">
					{#if status.client?.running}
						<Button variant="secondary" size="sm" loading={busy} onclick={() => clientAction('stop')}>
							Остановить
						</Button>
					{:else if status.client?.config}
						<Button variant="primary" size="sm" loading={busy} disabled={!status.installed} onclick={() => clientAction('start')}>
							Запустить
						</Button>
					{/if}
					<a class="proxy-nav-link" href="/proxy?tab=exit">Открыть в «Прокси» →</a>
				</div>
			</div>

			<div class="import-row">
				<Input
					label="Импорт ссылки openflux://v1/..."
					bind:value={clientLinkInput}
					placeholder="openflux://v1/..."
					fullWidth
				/>
				<Button variant="secondary" loading={parsingLink} disabled={!clientLinkInput.trim()} onclick={applyClientLink}>
					Импортировать
				</Button>
			</div>
		</Card>

		{#if logsOpen}
			<Card>
				<div class="section-head">
					<strong>Журнал OpenFlux (сервер)</strong>
					<Button variant="ghost" size="sm" onclick={toggleLogs}>Закрыть</Button>
				</div>
				<pre class="log-pre">{logs || 'Лог пуст'}</pre>
				{#if clientLogs}
					<div class="section-head" style="margin-top: 0.75rem;">
						<strong>Журнал OpenFlux (клиент)</strong>
					</div>
					<pre class="log-pre">{clientLogs}</pre>
				{/if}
			</Card>
		{/if}
	</div>
{/if}

<style>
	.of-grid {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.bar-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		flex-wrap: wrap;
	}

	.bar-left {
		display: flex;
		align-items: center;
		gap: 0.625rem;
		flex-wrap: wrap;
	}

	.bar-title {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
	}

	.sub {
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}

	.bar-actions,
	.btn-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
	}

	.err-banner {
		margin-top: 0.75rem;
		padding: 0.625rem 0.75rem;
		border-radius: var(--radius);
		background: var(--color-error-tint);
		border: 1px solid var(--color-error-border);
		color: var(--color-error);
		font-size: 0.8125rem;
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.section-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		margin-bottom: 0.75rem;
		flex-wrap: wrap;
	}

	.link-pre,
	.log-pre {
		margin: 0;
		padding: 0.625rem 0.75rem;
		border-radius: var(--radius);
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		font-family: var(--font-mono);
		font-size: 0.75rem;
		word-break: break-all;
		white-space: pre-wrap;
		max-height: 260px;
		overflow: auto;
	}

	.qr-wrap {
		margin-top: 0.75rem;
		display: flex;
		justify-content: flex-start;
	}

	.form-grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
		gap: 0.75rem;
	}

	.secret-cell {
		display: flex;
		align-items: flex-end;
		gap: 0.5rem;
	}

	.toggles-row {
		grid-column: 1 / -1;
		display: flex;
		flex-wrap: wrap;
		gap: 1rem;
		padding-top: 0.25rem;
	}

	.subhead {
		margin: 1rem 0 0.5rem;
		font-size: 0.75rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-secondary);
	}

	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}

	.chip {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		padding: 0.35rem 0.65rem;
		border-radius: 999px;
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-secondary);
		font-size: 0.8125rem;
		cursor: pointer;
	}

	.chip.active {
		border-color: var(--color-accent);
		background: color-mix(in srgb, var(--color-accent) 12%, transparent);
		color: var(--color-text-primary);
		font-weight: 500;
	}

	.save-bar {
		margin-top: 1rem;
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		flex-wrap: wrap;
	}

	.import-row {
		display: flex;
		align-items: flex-end;
		gap: 0.75rem;
		flex-wrap: wrap;
	}

	.proxy-nav-link {
		font-size: 0.8125rem;
		color: var(--color-accent);
		text-decoration: none;
	}

	.proxy-nav-link:hover {
		text-decoration: underline;
	}
</style>
