<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { Check, RefreshCw } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { effectiveStaticWan } from '$lib/api/proxyInstances';
	import { notifications } from '$lib/stores/notifications';
	import { errText } from '$lib/utils/errorMessage';
	import { createIngressMutationLock } from '$lib/utils/ingressMutation';
	import {
		Badge,
		Button,
		Card,
		ChipMultiSelect,
		Dropdown,
		FieldHint,
		Input,
		Stat,
		StatStrip,
		Toggle,
	} from '$lib/components/ui';
	import { ServerAccessPolicyDropdown } from '$lib/components/servers';
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
	import LinkBox from './LinkBox.svelte';
	import LogSection from './LogSection.svelte';
	import { cloneConfig } from './exitConfig';
	import { natModeOptions } from './shareConfig';
	import { ingressOn, nextIngressInterfaces, openfluxIngressRefs } from './shareIngress';
	import type { ProxyInstanceRow } from './rows';
	import type {
		AccessPolicy,
		OpenFluxConfig,
		OpenFluxStatus,
		SingboxRouterSettings,
		TunnelListItem,
	} from '$lib/types';

	interface Props {
		row: ProxyInstanceRow;
		status?: OpenFluxStatus | null;
		config?: OpenFluxConfig | null;
		routerClock?: string;
		policies: AccessPolicy[];
		tunnels: TunnelListItem[];
		busy?: boolean;
		onstart: () => void | Promise<void>;
		onstop: () => void | Promise<void>;
		onsave: (cfg: OpenFluxConfig) => Promise<void>;
		onreload: () => Promise<void>;
	}

	let {
		row,
		status = null,
		config = null,
		routerClock = '',
		policies,
		tunnels,
		busy = false,
		onstart,
		onstop,
		onsave,
		onreload,
	}: Props = $props();

	const TRANSPORTS = [
		{ id: 'webdav', label: 'WebDAV', hint: 'Собственное облако по WebDAV (высокая стабильность)' },
		{ id: 'mailru', label: 'Mail.ru Документы', hint: 'Общий документ Облака Mail.ru' },
		{ id: 'yandex', label: 'Яндекс Диск (rw)', hint: 'Редактируемый документ на Яндекс Диске' },
		{ id: 'vyandex', label: 'Яндекс Диск (view)', hint: 'Просмотровая ссылка + сессионный канал' },
		{ id: 'boards', label: 'Яндекс Доски', hint: 'Интерактивная доска Яндекс Досок' },
		{ id: 'avito', label: 'Avito', hint: 'Чат/объявление Avito + cookie-сессия' },
		{ id: 'cupsonline', label: 'CupsOnline', hint: 'Комната CupsOnline' },
		{ id: 'telemost', label: 'Яндекс Телемост', hint: 'Видеоконференция Телемоста' },
		{ id: 'oneme', label: 'MAX', hint: 'Транспорт MAX по токену и UID' },
		{ id: 'direct', label: 'Direct TCP', hint: 'Прямой TCP-сокет (резерв или без обфускации)' },
	] as const;

	function defaultDraft(): OpenFluxConfig {
		return {
			enabled: false,
			mode: 'l4',
			transportMode: 'session',
			transports: ['mailru', 'yandex', 'vyandex'],
			transportPriorities: {},
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
			natStaticWans: [],
			policy: '',
			lanSegments: [],
			exposeToPolicies: true,
			egressInterface: '',
			ndmsIface: '',
			tunIface: '',
			tunMtu: 1280,
		};
	}

	function normalizeDraft(src?: OpenFluxConfig | null): OpenFluxConfig {
		const base = defaultDraft();
		const m = src ? { ...base, ...cloneConfig(src) } : base;
		const wanList =
			m.natStaticWans && m.natStaticWans.length > 0
				? [...m.natStaticWans]
				: m.natStaticWan?.trim()
					? [m.natStaticWan.trim()]
					: [];
		return {
			...m,
			name: m.name ?? 'OpenFlux',
			mode: m.mode || 'l4',
			transportMode: m.transportMode || 'session',
			transports:
				Array.isArray(m.transports) && m.transports.length ? [...m.transports] : [...base.transports],
			transportPriorities: { ...(m.transportPriorities ?? {}) },
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
			directListen: m.directListen ?? '0.0.0.0:8445',
			directHost: m.directHost ?? '',
			secret: m.secret ?? '',
			codec: m.codec || 'batched',
			upstreamProxy: m.upstreamProxy ?? '',
			lowMemory: m.lowMemory ?? true,
			lanes: m.lanes ?? 0,
			nativeScriptsDir: m.nativeScriptsDir ?? '',
			natMode: m.natMode || 'full',
			natStaticWan: wanList[0] ?? '',
			natStaticWans: wanList,
			policy: m.policy ?? '',
			lanSegments: Array.isArray(m.lanSegments) ? [...m.lanSegments] : [],
			exposeToPolicies: m.exposeToPolicies ?? true,
			egressInterface: m.egressInterface ?? '',
			ndmsIface: m.ndmsIface ?? '',
			tunIface: m.tunIface ?? '',
			tunMtu: m.tunMtu ?? 1280,
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

	let draft = $state<OpenFluxConfig>(untrack(() => normalizeDraft(config ?? status?.config)));
	let dirty = $state(false);
	let saving = $state(false);
	let logs = $state('');
	let logsLoading = $state(false);

	let lanOptions = $state<{ value: string; label: string }[]>([]);
	let wanOptions = $state<{ value: string; label: string }[]>([]);
	let singboxRunning = $state(false);
	let ingress = $state(false);
	let ingressForced = $state(false);
	let ingressBusy = $state(false);
	const withIngressLock = createIngressMutationLock();

	const running = $derived(status?.running ?? row.state === 'running');
	const uptimeValue = $derived(running ? formatUptimeS(status?.uptimeS) : '—');
	const shareLink = $derived(status?.link ?? '');
	const natList = $derived(natModeOptions());
	const hasStaticWan = $derived(Boolean(effectiveStaticWan(draft).trim()));
	const egressOptions = $derived([
		{ value: '', label: 'По умолчанию (таблица маршрутизации)' },
		...wanOptions.map((w) => ({ value: w.value, label: `WAN: ${w.label}` })),
		...tunnels.map((t) => ({
			value: t.ndmsName || t.id,
			label: `Туннель: ${t.name} (${t.ndmsName || t.id})`,
		})),
	]);

	// Keep draft synced with backend poll while user has no unsaved edits.
	$effect(() => {
		const latest = config ?? status?.config;
		if (latest && !dirty) {
			draft = normalizeDraft(latest);
		}
	});

	async function loadLogs() {
		logsLoading = true;
		try {
			logs = (await api.openfluxGetLogs(150)).logs;
		} catch {
			/* ignore secondary */
		} finally {
			logsLoading = false;
		}
	}

	function allDevicesTProxy(s: SingboxRouterSettings): boolean {
		return (s.routingMode ?? 'tproxy') === 'tproxy' && s.deviceMode === 'all';
	}

	async function loadNetworkMeta() {
		try {
			const segs = await api.listManagedLANSegments();
			lanOptions = segs.map((s) => ({ value: s.name, label: s.label || s.name }));
		} catch {
			/* optional */
		}
		try {
			const wans = await api.getWANInterfaces();
			wanOptions = wans.map((w) => ({ value: w.name, label: w.label || w.name }));
		} catch {
			/* optional */
		}
		try {
			const [settings, sbStatus] = await Promise.all([
				api.singboxRouterGetSettings(),
				api.singboxGetStatus(),
			]);
			singboxRunning = sbStatus.running;
			ingress = ingressOn(settings.ingressInterfaces, openfluxIngressRefs(draft));
			ingressForced = allDevicesTProxy(settings);
		} catch {
			/* sing-box may be inactive in FIRC setups */
		}
	}

	onMount(() => {
		void loadNetworkMeta();
		void loadLogs();
	});

	function markDirty() {
		dirty = true;
	}

	function toggleTransport(id: string) {
		markDirty();
		if (draft.transports.includes(id)) {
			if (draft.transports.length <= 1) {
				notifications.warning('Оставьте хотя бы один транспорт');
				return;
			}
			draft.transports = draft.transports.filter((t) => t !== id);
		} else {
			draft.transports = [...draft.transports, id];
		}
	}

	function generateSecret() {
		markDirty();
		const arr = new Uint8Array(16);
		crypto.getRandomValues(arr);
		draft.secret = Array.from(arr, (b) => b.toString(16).padStart(2, '0')).join('');
	}

	function updateStaticWans(next: string[]) {
		markDirty();
		draft.natStaticWans = [...next];
		draft.natStaticWan = next[0] ?? '';
	}

	async function toggleIngress(on: boolean) {
		const refs = openfluxIngressRefs(draft);
		if (refs.length === 0) {
			notifications.info('Сначала запустите OpenFlux сервер в режиме L3, чтобы появился интерфейс opkgtun');
			return;
		}
		ingressBusy = true;
		try {
			await withIngressLock(async () => {
				const settings = await api.singboxRouterGetSettings();
				const next = nextIngressInterfaces(settings.ingressInterfaces, refs, on);
				await api.singboxRouterPutSettings({ ...settings, ingressInterfaces: next });
				ingress = on;
			});
			notifications.success(on ? 'Входной трафик L3 заведён в Sing-box' : 'Перехват трафика L3 отключён');
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			ingressBusy = false;
		}
	}

	async function handleSave() {
		if (draft.mode === 'l3' && draft.natMode === 'internet-only' && !hasStaticWan) {
			notifications.error('Для NAT «Только интернет» выберите хотя бы один WAN-интерфейс');
			return;
		}
		saving = true;
		try {
			const payload: OpenFluxConfig = {
				...cloneConfig(draft),
				mailruUrl: normalizeTransportUrls(draft.mailruUrl),
				mailruCookies: draft.mailruCookies?.trim() ?? '',
				yandexUrl: normalizeTransportUrls(draft.yandexUrl),
				vyandexUrl: normalizeTransportUrls(draft.vyandexUrl),
				boardsUrl: normalizeTransportUrls(draft.boardsUrl),
				avitoUrl: normalizeTransportUrls(draft.avitoUrl),
				cupsonlineUrl: normalizeTransportUrls(draft.cupsonlineUrl),
				telemostUrl: normalizeTransportUrls(draft.telemostUrl),
				upstreamProxy: draft.mode === 'l4' ? (draft.upstreamProxy?.trim() ?? '') : '',
				lanes: Math.max(0, Math.min(16, Number(draft.lanes) || 0)),
				tunMtu: Math.max(576, Number(draft.tunMtu) || 1280),
			};
			await onsave(payload);
			dirty = false;
			notifications.success('Настройки OpenFlux сервера сохранены');
			if (running) {
				notifications.info('Перезапустите сервер, чтобы применить обновлённые аргументы');
			}
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
</script>

<Card padding="lg">
	<div class="header">
		<div class="title-block">
			<div class="title-row">
				<h2>{row.name || 'OpenFlux'}</h2>
				<Badge size="sm" variant="info">OpenFlux</Badge>
				<Badge size="sm" variant="default">{draft.mode === 'l3' ? 'L3 · TUN' : 'L4 · Proxy'}</Badge>
				<InstanceBadges {row} />
			</div>
			<p class="subtitle">
				Выходная нода поверх доверенных российских облачных транспортов (без sing-box/FIRC зависимости)
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
		<Stat label="Состояние" value={running ? 'Работает' : 'Остановлен'} sub={status?.pid ? `PID ${status.pid}` : undefined} />
		<Stat label="Аптайм" value={uptimeValue} />
		<Stat label="Транспорты" value={`${draft.transports.length}`} sub={draft.transportMode === 'single' ? 'Single' : 'Session'} />
		<Stat label="Версия ядра" value={status?.version || '—'} sub={status?.arch} />
	</StatStrip>

	<LastErrorBox text={!running ? status?.error : ''} />

	{#if shareLink}
		<div class="link-section">
			<LinkBox link={shareLink} title="Ссылка профиля openflux://v1/ (для клиентов и роутеров)" />
		</div>
	{:else}
		<p class="link-empty">
			Задайте ключ шифрования (≥ 16 символов) и сохраните конфиг — здесь появится ссылка `openflux://v1/...` для подключения клиентов.
		</p>
	{/if}

	<DetailSection
		title="Режим и ядро"
		hint="L4 ретранслирует TCP/UDP через локальный SOCKS/direct или upstream-прокси; L3 поднимает интерфейс OpkgTun с NAT на роутере."
	>
		<div class="form-grid" oninput={markDirty}>
			<Input label="Название сервера" bind:value={draft.name} fullWidth />
			<Dropdown
				label="Режим работы"
				bind:value={draft.mode}
				options={[
					{ value: 'l4', label: 'L4 — Ретранслятор соединений' },
					{ value: 'l3', label: 'L3 — Маршрутизация через OpkgTun (TUN + NAT)' },
				]}
				onchange={markDirty}
				fullWidth
			/>
			<Dropdown
				label="Режим транспортов"
				bind:value={draft.transportMode}
				options={[
					{ value: 'session', label: 'Session (мультитранспорт + согласование)' },
					{ value: 'single', label: 'Single (один выбранный транспорт)' },
				]}
				onchange={markDirty}
				fullWidth
			/>
			<Dropdown
				label="Кодек фреймов"
				bind:value={draft.codec}
				options={[
					{ value: 'batched', label: 'batched (ускоренный, рекомендуемый)' },
					{ value: 'legacy', label: 'legacy (совместимость со старыми клиентами)' },
				]}
				onchange={markDirty}
				fullWidth
			/>

			<div class="secret-wrap">
				<SensitiveInput label="Ключ шифрования (Secret ≥ 16 символов)" bind:value={draft.secret} />
				<Button variant="ghost" size="sm" onclick={generateSecret}>
					<RefreshCw size={14} />
					Сгенерировать
				</Button>
			</div>

			{#if draft.mode === 'l4'}
				<Input
					label="Upstream Proxy (--upstream-proxy)"
					bind:value={draft.upstreamProxy}
					placeholder="socks5://127.0.0.1:1080"
					hint="Опционально: направлять весь исходящий трафик L4-ноды через внешний локальный прокси (sing-box / FIRC)."
					fullWidth
				/>
			{/if}

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
				hint="Путь к кастомным браузерным скриптам на устройстве"
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
					label="Экономия ОЗУ (--low-memory)"
					hint="Снижает буферы ядра для роутеров с 128–256 МБ RAM"
					checked={draft.lowMemory ?? true}
					onchange={(v) => {
						markDirty();
						draft.lowMemory = v;
					}}
				/>
				<Toggle
					label="Открывать порт Direct в firewall"
					checked={draft.openFirewall}
					onchange={(v) => {
						markDirty();
						draft.openFirewall = v;
					}}
				/>
			</div>
		</div>
	</DetailSection>

	<DetailSection
		title="Транспорты"
		hint="В режиме Session можно включить сразу несколько транспортов — сервер и клиент переключатся на рабочий автоматически."
	>
		<div class="transport-chips">
			{#each TRANSPORTS as tr (tr.id)}
				<button
					type="button"
					class="t-chip"
					class:active={draft.transports.includes(tr.id)}
					title={tr.hint}
					onclick={() => toggleTransport(tr.id)}
				>
					{#if draft.transports.includes(tr.id)}<Check size={13} />{/if}
					{tr.label}
				</button>
			{/each}
		</div>

		<div class="form-grid" style="margin-top: 0.875rem;" oninput={markDirty}>
			{#if draft.transports.includes('webdav')}
				<Input label="WebDAV URL" bind:value={draft.webdavUrl} placeholder="https://dav.example.com/remote.php/dav/files/user/room" fullWidth />
				<Input label="WebDAV логин" bind:value={draft.webdavUser} fullWidth />
				<SensitiveInput label="WebDAV пароль" bind:value={draft.webdavPassword} />
			{/if}
			{#if draft.transports.includes('mailru')}
				<MultiUrlTextarea
					label="Mail.ru Документы URL"
					bind:value={draft.mailruUrl}
					placeholder={"https://cloud.mail.ru/public/AAA/111\nhttps://cloud.mail.ru/public/BBB/222"}
					hint="Один документ даёт ~270 КБ/с. Укажите 2–8 ссылок (по одной на строку или через запятую) для параллельного ускорения (stripe)."
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
					label="Яндекс Диск (редактирование) URL"
					bind:value={draft.yandexUrl}
					placeholder={"https://disk.yandex.ru/edit/...\nhttps://disk.yandex.ru/edit/..."}
					oninput={markDirty}
				/>
			{/if}
			{#if draft.transports.includes('vyandex')}
				<MultiUrlTextarea
					label="Яндекс Диск (просмотр) URL"
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
					label="Avito URL / диалог"
					bind:value={draft.avitoUrl}
					placeholder={"https://www.avito.ru/...\nhttps://www.avito.ru/..."}
					oninput={markDirty}
				/>
				<Input label="Avito Cookies" bind:value={draft.avitoCookies} placeholder="sessid=..." fullWidth />
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
				<Input label="Direct Listen" bind:value={draft.directListen} placeholder="0.0.0.0:8445" fullWidth />
				<Input label="Direct Host (публичный адрес для ссылки)" bind:value={draft.directHost} placeholder="home.example.com:8445" fullWidth />
			{/if}
		</div>
	</DetailSection>

	{#if draft.mode === 'l3'}
		<DetailSection
			title="Сеть L3 и маршрутизация"
			hint="Настройки OpkgTun интерфейса и правил NAT/перехвата трафика подключающихся клиентов."
		>
			<div class="form-grid" oninput={markDirty}>
				<Dropdown
					label="Режим NAT"
					value={draft.natMode ?? 'full'}
					options={natList}
					onchange={(v) => {
						markDirty();
						draft.natMode = v;
					}}
					fullWidth
				/>

				{#if draft.natMode === 'internet-only'}
					<div class="field-span">
						<span class="field-lbl">Разрешённые WAN-интерфейсы</span>
						<ChipMultiSelect
							values={draft.natStaticWans ?? []}
							options={wanOptions}
							placeholder="Выберите WAN-интерфейсы"
							onchange={updateStaticWans}
						/>
					</div>
				{/if}

				<div class="field-span">
					<span class="field-lbl">Сегменты LAN (для доступа клиентов)</span>
					<ChipMultiSelect
						values={draft.lanSegments ?? []}
						options={lanOptions}
						placeholder="Все локальные сегменты по умолчанию"
						onchange={(next) => {
							markDirty();
							draft.lanSegments = next;
						}}
					/>
				</div>

				<div class="field-span">
					<span class="field-lbl">Политика доступа (NDMS)</span>
					<ServerAccessPolicyDropdown
						labelless
						policy={draft.policy || 'none'}
						onchange={(v) => {
							markDirty();
							draft.policy = v === 'none' ? '' : v;
						}}
					/>
				</div>

				<Dropdown
					label="Исходящий интерфейс (Egress)"
					value={draft.egressInterface ?? ''}
					options={egressOptions}
					onchange={(v) => {
						markDirty();
						draft.egressInterface = v;
					}}
					fullWidth
				/>

				<Input
					label="MTU туннеля"
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

				<div class="toggle-grid">
					<Toggle
						label="Показывать интерфейс политикам (ip global auto)"
						checked={draft.exposeToPolicies ?? true}
						onchange={(v) => {
							markDirty();
							draft.exposeToPolicies = v;
						}}
					/>
					{#if singboxRunning || ingressForced || ingress}
						<Toggle
							label="Перехватывать трафик в Sing-box (TProxy ingress)"
							checked={ingress || ingressForced}
							disabled={ingressBusy || ingressForced}
							hint={ingressForced
								? 'Режим Sing-box «все устройства» перехватывает все интерфейсы автоматически.'
								: 'Добавить OpkgTun интерфейс OpenFlux в ingress Sing-box.'}
							onchange={(v) => void toggleIngress(v)}
						/>
					{/if}
				</div>
			</div>
		</DetailSection>
	{/if}

	<DetailSection title="Отладка" collapsed>
		<div class="form-grid">
			<Dropdown
				label="Уровень логирования (-d)"
				value={String(draft.debug ?? 0)}
				options={[
					{ value: '0', label: '0 — Стандартный (ошибки и события)' },
					{ value: '1', label: '1 (-d) — Подробный' },
					{ value: '2', label: '2 (-dd) — Отладочный' },
					{ value: '3', label: '3 (-ddd) — Максимальный (трассировка пакетов)' },
				]}
				onchange={(v) => {
					markDirty();
					draft.debug = Number(v) || 0;
				}}
				fullWidth
			/>
		</div>
	</DetailSection>

	<div class="save-bar">
		<div class="save-left">
			{#if dirty}
				<span class="dirty-note">Есть несохранённые изменения<FieldHint text="Не забудьте нажать «Сохранить», чтобы записать настройки." ariaLabel="Подсказка сохранения" /></span>
			{/if}
		</div>
		<div class="save-actions">
			{#if dirty}
				<Button variant="ghost" disabled={saving} onclick={handleRevert}>Отменить</Button>
			{/if}
			<Button variant="primary" loading={saving} onclick={handleSave}>Сохранить</Button>
		</div>
	</div>

	<LogSection
		log={logs}
		{routerClock}
		hint="Последние строки /opt/var/log/openflux.log (обновляются при сохранении или кнопкой «Обновить»)."
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

	.link-section {
		margin: 1rem 0 0.5rem;
	}

	.link-empty {
		margin: 0.75rem 0;
		font-size: 0.8125rem;
		color: var(--color-text-muted);
	}

	.form-grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(230px, 1fr));
		gap: 0.75rem;
	}

	.field-span {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
	}

	.field-lbl {
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--color-text-secondary);
	}

	.secret-wrap {
		display: flex;
		align-items: flex-end;
		gap: 0.5rem;
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
	}

	.log-refresh-row {
		margin-top: 0.35rem;
		display: flex;
		justify-content: flex-end;
	}
</style>
