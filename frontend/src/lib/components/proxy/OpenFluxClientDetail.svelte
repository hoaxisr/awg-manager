<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { Check, Copy, ExternalLink, RefreshCw, Trash2 } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { findPolicyForInterface } from '$lib/utils/accessPolicy';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { errText } from '$lib/utils/errorMessage';
	import {
		Badge,
		Button,
		Card,
		Dropdown,
		FieldHint,
		Input,
		Stat,
		StatStrip,
		Toggle,
	} from '$lib/components/ui';
	import SensitiveInput from '../proxy-panel/SensitiveInput.svelte';
	import {
		CookieTextarea,
		MultiUrlTextarea,
		formatUrlsForTextarea,
		normalizeTransportUrls,
	} from '../openflux';
	import DetailSection from './DetailSection.svelte';
	import InstanceBadges from './InstanceBadges.svelte';
	import LastErrorBox from './LastErrorBox.svelte';
	import LogSection from './LogSection.svelte';
	import { cloneConfig } from './exitConfig';
	import type { ProxyInstanceRow } from './rows';
	import type { AccessPolicy, OpenFluxClientConfig, OpenFluxClientStatus } from '$lib/types';

	interface Props {
		row: ProxyInstanceRow;
		status?: OpenFluxClientStatus | null;
		config?: OpenFluxClientConfig | null;
		routerClock?: string;
		policies: AccessPolicy[];
		busy?: boolean;
		onstart: () => void | Promise<void>;
		onstop: () => void | Promise<void>;
		ondelete?: () => void;
		onsave: (cfg: OpenFluxClientConfig) => Promise<OpenFluxClientConfig>;
		onreload: () => Promise<void>;
	}

	let {
		row,
		status = null,
		config = null,
		routerClock = '',
		policies,
		busy = false,
		onstart,
		onstop,
		ondelete,
		onsave,
		onreload,
	}: Props = $props();

	const TRANSPORTS = [
		{ id: 'webdav', label: 'WebDAV' },
		{ id: 'mailru', label: 'Mail.ru Документы' },
		{ id: 'yandex', label: 'Яндекс Диск (rw)' },
		{ id: 'vyandex', label: 'Яндекс Диск (view)' },
		{ id: 'boards', label: 'Яндекс Доски' },
		{ id: 'avito', label: 'Avito' },
		{ id: 'cupsonline', label: 'CupsOnline' },
		{ id: 'telemost', label: 'Яндекс Телемост' },
		{ id: 'oneme', label: 'MAX' },
		{ id: 'direct', label: 'Direct TCP' },
	] as const;

	function defaultClientDraft(): OpenFluxClientConfig {
		return {
			enabled: false,
			name: 'OpenFlux Выход',
			inboundMode: 'tun',
			ndmsIface: '',
			tunIface: '',
			tunAddr: '10.10.10.2/24',
			tunMtu: 1280,
			policy: '',
			exposeToPolicies: true,
			defaultRoute: false,
			transportMode: 'session',
			transports: ['mailru', 'yandex', 'vyandex'],
			directDial: '',
			webdavUrl: '',
			webdavUser: '',
			webdavPassword: '',
			mailruUrl: '',
			mailruCookies: '',
			yandexUrl: '',
			vyandexUrl: '',
			boardsUrl: '',
			avitoUrl: '',
			avitoCookies: '',
			cupsonlineUrl: '',
			telemostUrl: '',
			onemeToken: '',
			onemeUid: '',
			secret: '',
			context: '',
			codec: 'batched',
			lowMemory: true,
			lanes: 0,
			nativeScriptsDir: '',
			socks5Listen: '127.0.0.1:1085',
			httpProxyListen: '',
			autoReconnect: true,
			debug: 0,
		};
	}

	function normalizeDraft(src?: OpenFluxClientConfig | null): OpenFluxClientConfig {
		const base = defaultClientDraft();
		const m = src ? { ...base, ...cloneConfig(src) } : base;
		return {
			...m,
			name: m.name ?? 'OpenFlux Выход',
			inboundMode: m.inboundMode || 'tun',
			ndmsIface: m.ndmsIface ?? '',
			tunIface: m.tunIface ?? '',
			tunAddr: m.tunAddr || '10.10.10.2/24',
			tunMtu: m.tunMtu ?? 1280,
			policy: m.policy ?? '',
			exposeToPolicies: m.exposeToPolicies ?? true,
			defaultRoute: m.defaultRoute ?? false,
			transportMode: m.transportMode || 'session',
			transports:
				Array.isArray(m.transports) && m.transports.length ? [...m.transports] : [...base.transports],
			directDial: m.directDial ?? '',
			webdavUrl: m.webdavUrl ?? '',
			webdavUser: m.webdavUser ?? '',
			webdavPassword: m.webdavPassword ?? '',
			mailruUrl: formatUrlsForTextarea(m.mailruUrl),
			mailruCookies: m.mailruCookies ?? '',
			yandexUrl: formatUrlsForTextarea(m.yandexUrl),
			vyandexUrl: formatUrlsForTextarea(m.vyandexUrl),
			boardsUrl: formatUrlsForTextarea(m.boardsUrl),
			avitoUrl: formatUrlsForTextarea(m.avitoUrl),
			avitoCookies: m.avitoCookies ?? '',
			cupsonlineUrl: formatUrlsForTextarea(m.cupsonlineUrl),
			telemostUrl: formatUrlsForTextarea(m.telemostUrl),
			onemeToken: m.onemeToken ?? '',
			onemeUid: m.onemeUid ?? '',
			secret: m.secret ?? '',
			context: m.context ?? '',
			codec: m.codec || 'batched',
			lowMemory: m.lowMemory ?? true,
			lanes: m.lanes ?? 0,
			nativeScriptsDir: m.nativeScriptsDir ?? '',
			socks5Listen: m.socks5Listen || '127.0.0.1:1085',
			httpProxyListen: m.httpProxyListen ?? '',
			autoReconnect: m.autoReconnect ?? true,
			debug: m.debug ?? 0,
		};
	}

	function formatUptimeS(sec?: number): string {
		if (!sec || sec <= 0) return '—';
		const h = Math.floor(sec / 3600);
		const min = Math.floor((sec % 3600) / 60);
		const s = sec % 60;
		if (h > 0) return `${h}ч ${min}м`;
		if (min > 0) return `${min}м ${s}с`;
		return `${s}с`;
	}

	let draft = $state<OpenFluxClientConfig>(untrack(() => normalizeDraft(config ?? status?.config)));
	let dirty = $state(false);
	let saving = $state(false);
	let linkImport = $state('');
	let parsingLink = $state(false);
	let logs = $state('');
	let logsLoading = $state(false);

	const running = $derived(status?.running ?? row.state === 'running');
	const uptimeValue = $derived(running ? formatUptimeS(status?.uptimeS) : '—');
	const isSocks = $derived(draft.inboundMode === 'socks5');
	const ndmsIface = $derived(status?.ndmsIface || draft.ndmsIface || '');
	const matchedPolicy = $derived(
		ndmsIface ? findPolicyForInterface(policies, ndmsIface) : null,
	);
	const socksUri = $derived(
		status?.socks5Uri || (draft.socks5Listen ? `socks5://${draft.socks5Listen}` : ''),
	);
	const httpUri = $derived(
		status?.httpUri || (draft.httpProxyListen ? `http://${draft.httpProxyListen}` : ''),
	);

	$effect(() => {
		const latest = config ?? status?.config;
		if (latest && !dirty) {
			draft = normalizeDraft(latest);
		}
	});

	async function loadLogs() {
		logsLoading = true;
		try {
			logs = (await api.openfluxGetClientLogs(150)).logs;
		} catch {
			/* secondary */
		} finally {
			logsLoading = false;
		}
	}

	onMount(() => {
		void loadLogs();
	});

	function markDirty() {
		dirty = true;
	}

	function toggleTransport(id: string) {
		markDirty();
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

	async function handleParseLink() {
		const raw = linkImport.trim();
		if (!raw) return;
		parsingLink = true;
		try {
			const res = await api.openfluxParseLink(raw);
			const parsed = normalizeDraft(res.config);
			// Keep local router inbound/memory settings unless untouched
			draft = {
				...parsed,
				inboundMode: draft.inboundMode || parsed.inboundMode,
				tunAddr: draft.tunAddr || parsed.tunAddr,
				tunMtu: draft.tunMtu || parsed.tunMtu,
				socks5Listen: draft.socks5Listen || parsed.socks5Listen,
				httpProxyListen: draft.httpProxyListen || parsed.httpProxyListen,
				lowMemory: draft.lowMemory ?? true,
				lanes: draft.lanes ?? parsed.lanes,
				nativeScriptsDir: draft.nativeScriptsDir || parsed.nativeScriptsDir,
				enabled: draft.enabled,
			};
			linkImport = '';
			dirty = true;
			notifications.success('Параметры из ссылки openflux:// подставлены — нажмите «Сохранить»');
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			parsingLink = false;
		}
	}

	async function handleSave() {
		saving = true;
		try {
			const payload: OpenFluxClientConfig = {
				...cloneConfig(draft),
				mailruUrl: normalizeTransportUrls(draft.mailruUrl),
				mailruCookies: draft.mailruCookies?.trim() ?? '',
				yandexUrl: normalizeTransportUrls(draft.yandexUrl),
				vyandexUrl: normalizeTransportUrls(draft.vyandexUrl),
				boardsUrl: normalizeTransportUrls(draft.boardsUrl),
				avitoUrl: normalizeTransportUrls(draft.avitoUrl),
				cupsonlineUrl: normalizeTransportUrls(draft.cupsonlineUrl),
				telemostUrl: normalizeTransportUrls(draft.telemostUrl),
				lanes: Math.max(0, Math.min(16, Number(draft.lanes) || 0)),
				tunMtu: Math.max(576, Number(draft.tunMtu) || 1280),
			};
			const saved = await onsave(payload);
			draft = normalizeDraft(saved);
			dirty = false;
			notifications.success('Настройки клиента OpenFlux сохранены');
			if (running) {
				notifications.info('Перезапустите клиент для применения обновлённых параметров');
			}
			await onreload();
			await loadLogs();
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			saving = false;
		}
	}

	function handleRevert() {
		draft = normalizeDraft(config ?? status?.config);
		dirty = false;
	}

	async function copyText(val: string, label: string) {
		if (!val) return;
		if (await copyToClipboard(val)) {
			notifications.success(`${label} скопирован`);
		}
	}
</script>

<Card padding="lg">
	<div class="header">
		<div class="title-block">
			<div class="title-row">
				<h2>{row.name || draft.name || 'OpenFlux Выход'}</h2>
				<Badge size="sm" variant="info">OpenFlux</Badge>
				<Badge size="sm" variant="default">{isSocks ? 'SOCKS5 / HTTP' : 'OpkgTun · L3'}</Badge>
				<InstanceBadges {row} />
			</div>
			<p class="subtitle">
				Клиент туннелирования через облачные транспорты ({isSocks ? 'локальный прокси для sing-box / FIRC' : 'системный интерфейс OpkgTun'})
			</p>
		</div>

		<div class="head-actions">
			<Toggle
				checked={running}
				controlled
				loading={busy}
				disabled={!row.binaryPresent || busy}
				label={running ? 'Запущен' : 'Остановлен'}
				onchange={(on) => (on ? onstart() : onstop())}
			/>
		</div>
	</div>

	<StatStrip>
		<Stat
			label="Состояние"
			value={running ? 'Работает' : 'Остановлен'}
			sub={status?.pid ? `PID ${status.pid}` : undefined}
		/>
		<Stat label="Аптайм" value={uptimeValue} />
		<Stat
			label={isSocks ? 'Прокси-вход' : 'Интерфейс'}
			value={isSocks ? `:${status?.socks5Port || 1085}` : ndmsIface || status?.tunIface || 'OpkgTun'}
		/>
		<Stat label="Транспорты" value={`${draft.transports.length}`} sub={draft.transportMode} />
	</StatStrip>

	<LastErrorBox text={!running ? status?.error : ''} />

	{#if isSocks}
		<div class="proxy-box">
			<div class="proxy-box-head">
				<strong>Локальный прокси-выход для sing-box / FIRC</strong>
				<a class="routing-link" href="/routing">
					Настроить в «Маршрутизации» <ExternalLink size={13} />
				</a>
			</div>
			<div class="proxy-uris">
				{#if socksUri}
					<div class="uri-pill">
						<code>{socksUri}</code>
						<Button variant="ghost" size="sm" onclick={() => copyText(socksUri, 'SOCKS5 URI')}>
							<Copy size={13} />
						</Button>
					</div>
				{/if}
				{#if httpUri}
					<div class="uri-pill">
						<code>{httpUri}</code>
						<Button variant="ghost" size="sm" onclick={() => copyText(httpUri, 'HTTP URI')}>
							<Copy size={13} />
						</Button>
					</div>
				{/if}
			</div>
		</div>
	{:else if ndmsIface}
		<div class="policy-note">
			<span>
				Интерфейс роутера: <strong>{ndmsIface}</strong>
				{#if matchedPolicy}
					· в политике «{matchedPolicy.description || matchedPolicy.name}»
				{:else}
					· не назначен политике доступа
				{/if}
			</span>
			<a class="routing-link" href="/routing">
				Открыть «Маршрутизацию» <ExternalLink size={13} />
			</a>
		</div>
	{/if}

	<DetailSection title="Импорт профиля" collapsed>
		<div class="import-row">
			<Input
				label="Ссылка openflux://v1/..."
				bind:value={linkImport}
				placeholder="openflux://v1/..."
				fullWidth
			/>
			<Button variant="secondary" loading={parsingLink} disabled={!linkImport.trim()} onclick={handleParseLink}>
				Разобрать
			</Button>
		</div>
	</DetailSection>

	<DetailSection
		title="Режим входа и подключения"
		hint="В режиме OpkgTun создаётся системный L3-интерфейс роутера (виден в туннелях и политиках Keenetic); в режиме SOCKS5/HTTP открываются локальные порты для sing-box / FIRC."
	>
		<div class="form-grid" oninput={markDirty}>
			<Input label="Название" bind:value={draft.name} fullWidth />
			<Dropdown
				label="Режим входа"
				value={draft.inboundMode ?? 'tun'}
				options={[
					{ value: 'tun', label: 'OpkgTun (системный L3-интерфейс роутера)' },
					{ value: 'socks5', label: 'SOCKS5 / HTTP (локальный прокси для sing-box / FIRC)' },
				]}
				onchange={(v) => {
					markDirty();
					draft.inboundMode = v;
				}}
				fullWidth
			/>

			{#if isSocks}
				<Input label="SOCKS5 Listen" bind:value={draft.socks5Listen} placeholder="127.0.0.1:1085" fullWidth />
				<Input label="HTTP Proxy Listen (опц.)" bind:value={draft.httpProxyListen} placeholder="127.0.0.1:1086" fullWidth />
			{:else}
				<Input label="Адрес TUN (CIDR)" bind:value={draft.tunAddr} placeholder="10.10.10.2/24" fullWidth />
				<Input
					label="MTU"
					type="number"
					min="576"
					max="1500"
					value={String(draft.tunMtu ?? 1280)}
					oninput={(v) => {
						markDirty();
						draft.tunMtu = Number(v) || 1280;
					}}
					fullWidth
				/>
			{/if}

			<Dropdown
				label="Режим транспорта"
				bind:value={draft.transportMode}
				options={[
					{ value: 'session', label: 'Session (автосогласование)' },
					{ value: 'single', label: 'Single' },
					{ value: 'stream', label: 'Stream' },
				]}
				onchange={markDirty}
				fullWidth
			/>
			<Dropdown
				label="Кодек"
				bind:value={draft.codec}
				options={[
					{ value: 'batched', label: 'batched (рекомендуется)' },
					{ value: 'legacy', label: 'legacy' },
				]}
				onchange={markDirty}
				fullWidth
			/>

			<SensitiveInput label="Ключ шифрования (Secret)" bind:value={draft.secret} />
			<Input label="Context (--context, опц.)" bind:value={draft.context} fullWidth />

			<Input
				label="Параллельные потоки (--lanes)"
				type="number"
				min="0"
				max="16"
				value={String(draft.lanes ?? 0)}
				oninput={(v) => {
					markDirty();
					draft.lanes = Math.max(0, Math.min(16, Number(v) || 0));
				}}
				hint="0 = авто; только для автосоздания комнат (CupsOnline / Avito без URL). Для Mail.ru / Яндекс число линий задаётся количеством ссылок."
				fullWidth
			/>
			<Input
				label="Каталог нативных скриптов (--native-scripts)"
				bind:value={draft.nativeScriptsDir}
				placeholder="/opt/etc/openflux/scripts"
				fullWidth
			/>

			<div class="toggle-grid">
				<Toggle
					label="Автозапуск (enabled)"
					checked={draft.enabled}
					onchange={(v) => {
						markDirty();
						draft.enabled = v;
					}}
				/>
				<Toggle
					label="Автопереподключение"
					checked={draft.autoReconnect}
					onchange={(v) => {
						markDirty();
						draft.autoReconnect = v;
					}}
				/>
				<Toggle
					label="Экономия ОЗУ (--low-memory)"
					checked={draft.lowMemory ?? true}
					onchange={(v) => {
						markDirty();
						draft.lowMemory = v;
					}}
				/>
				{#if !isSocks}
					<Toggle
						label="Показывать политикам (ip global auto)"
						checked={draft.exposeToPolicies ?? true}
						onchange={(v) => {
							markDirty();
							draft.exposeToPolicies = v;
						}}
					/>
					<Toggle
						label="Кандидат default route"
						checked={draft.defaultRoute ?? false}
						onchange={(v) => {
							markDirty();
							draft.defaultRoute = v;
						}}
					/>
				{/if}
			</div>
		</div>
	</DetailSection>

	<DetailSection title="Транспорты клиента" hint="Выберите транспорты из ссылки сервера или задайте вручную.">
		<div class="transport-chips">
			{#each TRANSPORTS as tr (tr.id)}
				<button
					type="button"
					class="t-chip"
					class:active={draft.transports.includes(tr.id)}
					onclick={() => toggleTransport(tr.id)}
				>
					{#if draft.transports.includes(tr.id)}<Check size={13} />{/if}
					{tr.label}
				</button>
			{/each}
		</div>

		<div class="form-grid" style="margin-top: 0.875rem;" oninput={markDirty}>
			{#if draft.transports.includes('webdav')}
				<Input label="WebDAV URL" bind:value={draft.webdavUrl} fullWidth />
				<Input label="WebDAV логин" bind:value={draft.webdavUser} fullWidth />
				<SensitiveInput label="WebDAV пароль" bind:value={draft.webdavPassword} />
			{/if}
			{#if draft.transports.includes('mailru')}
				<MultiUrlTextarea
					label="Mail.ru Документы URL"
					bind:value={draft.mailruUrl}
					placeholder={"https://cloud.mail.ru/public/AAA/111\nhttps://cloud.mail.ru/public/BBB/222"}
					hint="Один документ даёт ~270 КБ/с. Укажите 2–8 ссылок (по одной на строку или через запятую) — столько же и в том же наборе, что на сервере."
					oninput={markDirty}
				/>
				<CookieTextarea
					label="Mail.ru Cookies (обязательно для r7/edit)"
					bind:value={draft.mailruCookies}
					hint="Mail.ru требует авторизацию для /api/v4/r7/edit (HTTP 401 без cookies). Вставьте заголовок Cookie: (с Mpop=...), Netscape cookies.txt или JSON из Cookie-Editor. Не попадает в ссылку openflux://; можно использовать один технический аккаунт на сервере и клиенте."
					oninput={markDirty}
				/>
			{/if}
			{#if draft.transports.includes('yandex')}
				<MultiUrlTextarea
					label="Яндекс Диск (rw) URL"
					bind:value={draft.yandexUrl}
					placeholder={"https://disk.yandex.ru/edit/...\nhttps://disk.yandex.ru/edit/..."}
					oninput={markDirty}
				/>
			{/if}
			{#if draft.transports.includes('vyandex')}
				<MultiUrlTextarea
					label="Яндекс Диск (view) URL"
					bind:value={draft.vyandexUrl}
					placeholder={"https://disk.yandex.ru/i/...\nhttps://disk.yandex.ru/i/..."}
					oninput={markDirty}
				/>
			{/if}
			{#if draft.transports.includes('boards')}
				<MultiUrlTextarea
					label="Яндекс Доски URL"
					bind:value={draft.boardsUrl}
					placeholder={"https://boards.yandex.ru/...\nhttps://boards.yandex.ru/..."}
					oninput={markDirty}
				/>
			{/if}
			{#if draft.transports.includes('avito')}
				<MultiUrlTextarea
					label="Avito URL"
					bind:value={draft.avitoUrl}
					placeholder={"https://www.avito.ru/...\nhttps://www.avito.ru/..."}
					oninput={markDirty}
				/>
				<Input label="Avito Cookies" bind:value={draft.avitoCookies} fullWidth />
			{/if}
			{#if draft.transports.includes('cupsonline')}
				<MultiUrlTextarea
					label="CupsOnline URL"
					bind:value={draft.cupsonlineUrl}
					placeholder="Список комнат или URL через запятую/перенос строки"
					oninput={markDirty}
				/>
			{/if}
			{#if draft.transports.includes('telemost')}
				<MultiUrlTextarea
					label="Яндекс Телемост URL"
					bind:value={draft.telemostUrl}
					placeholder={"https://telemost.yandex.ru/j/...\nhttps://telemost.yandex.ru/j/..."}
					oninput={markDirty}
				/>
			{/if}
			{#if draft.transports.includes('oneme')}
				<Input label="MAX Token (--maxToken)" bind:value={draft.onemeToken} fullWidth />
				<Input label="MAX UID (--maxUid)" bind:value={draft.onemeUid} fullWidth />
			{/if}
			{#if draft.transports.includes('direct')}
				<Input label="Direct Dial (host:port сервера)" bind:value={draft.directDial} placeholder="1.2.3.4:8445" fullWidth />
			{/if}
		</div>
	</DetailSection>

	<div class="save-bar">
		<div class="save-left">
			{#if dirty}
				<span class="dirty-note">
					Есть несохранённые изменения
					<FieldHint text="Нажмите «Сохранить», чтобы записать конфиг клиента OpenFlux." ariaLabel="Подсказка сохранения" />
				</span>
			{/if}
		</div>
		<div class="save-actions">
			{#if ondelete}
				<Button variant="ghost" size="sm" onclick={ondelete}>
					{#snippet iconBefore()}<Trash2 size={14} />{/snippet}
					Удалить
				</Button>
			{/if}
			{#if dirty}
				<Button variant="ghost" disabled={saving} onclick={handleRevert}>Отменить</Button>
			{/if}
			<Button variant="primary" loading={saving} onclick={handleSave}>Сохранить</Button>
		</div>
	</div>

	<LogSection
		log={logs}
		{routerClock}
		showDebug
		debug={(draft.debug ?? 0) > 0}
		debugHint="Включает флаг -d в клиенте OpenFlux (применится после сохранения и перезапуска)."
		ondebug={(on) => {
			markDirty();
			draft.debug = on ? 1 : 0;
		}}
	/>
	<div class="log-refresh-row">
		<Button variant="ghost" size="sm" loading={logsLoading} onclick={loadLogs}>
			{#snippet iconBefore()}<RefreshCw size={13} />{/snippet}
			Обновить журнал
		</Button>
	</div>
</Card>

<style>
	.header {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 1rem;
		flex-wrap: wrap;
		margin-bottom: 1rem;
	}

	.title-block {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.title-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
	}

	.title-row h2 {
		margin: 0;
		font-size: 1.125rem;
		font-weight: 700;
	}

	.subtitle {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}

	.proxy-box,
	.policy-note {
		margin: 0.875rem 0;
		padding: 0.75rem 0.875rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		background: var(--color-bg-secondary);
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		font-size: 0.8125rem;
	}

	.policy-note {
		flex-direction: row;
		align-items: center;
		justify-content: space-between;
		flex-wrap: wrap;
	}

	.proxy-box-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		flex-wrap: wrap;
	}

	.proxy-uris {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}

	.uri-pill {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		padding: 0.25rem 0.5rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		background: var(--color-bg-tertiary);
	}

	.routing-link {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		color: var(--color-accent);
		text-decoration: none;
		font-weight: 500;
	}

	.routing-link:hover {
		text-decoration: underline;
	}

	.import-row {
		display: flex;
		align-items: flex-end;
		gap: 0.75rem;
		flex-wrap: wrap;
	}

	.form-grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(230px, 1fr));
		gap: 0.75rem;
	}

	.toggle-grid {
		grid-column: 1 / -1;
		display: flex;
		flex-wrap: wrap;
		gap: 1rem;
		padding-top: 0.25rem;
	}

	.transport-chips {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}

	.t-chip {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		padding: 0.35rem 0.7rem;
		border-radius: 999px;
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-secondary);
		font-size: 0.8125rem;
		cursor: pointer;
	}

	.t-chip.active {
		border-color: var(--color-accent);
		background: color-mix(in srgb, var(--color-accent) 12%, transparent);
		color: var(--color-text-primary);
		font-weight: 500;
	}

	.save-bar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		padding: 0.875rem 0 0.25rem;
		border-top: 1px solid var(--color-border);
		margin-top: 0.5rem;
		flex-wrap: wrap;
	}

	.dirty-note {
		font-size: 0.8125rem;
		color: var(--color-warning);
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
	}

	.save-actions {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
	}

	.log-refresh-row {
		margin-top: 0.35rem;
		display: flex;
		justify-content: flex-end;
	}
</style>
