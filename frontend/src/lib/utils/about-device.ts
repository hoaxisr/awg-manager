import { browser } from '$app/environment';
import type {
	AccessPolicy,
	DeviceProxyConfig,
	DeviceProxyRuntime,
	DnsCheckStartResponse,
	HydraRouteStatus,
	PolicyDevice,
	Settings,
	SingboxStatus,
	Subscription,
	SystemInfo,
} from '$lib/types';
import type { UsageLevel } from '$lib/types/usageLevel';
import { USAGE_LEVEL_LABELS } from '$lib/types/usageLevel';
import type { ThemeState } from '$lib/stores/theme';

export interface AboutInfoRow {
	label: string;
	value: string;
	/** Hint shown on hover (e.g. full user agent). */
	title?: string;
}

export interface BrowserSnapshot {
	userAgent: string;
	platform: string;
	languages: string;
	timezone: string;
	screen: string;
	viewport: string;
	/** Грубая оценка масштаба страницы: outerWidth / innerWidth. */
	zoom: string;
	devicePixelRatio: string;
	colorDepth: string;
	prefersColorScheme: string;
	hardwareConcurrency: string;
	onLine: string;
	secureContext: string;
	maxTouchPoints: string;
	connection: string;
	usageLevelAttr: string;
	pageUrl: string;
}

export type PolicyNameLookup = ReadonlyMap<string, string>;

export interface RouterClientContext {
	clientIP: string;
	hostname: string;
	policyMessage: string;
	device: PolicyDevice | null;
	fromRouter: boolean;
	policyLookup?: PolicyNameLookup;
}

export function buildPolicyNameLookup(policies: AccessPolicy[]): PolicyNameLookup {
	const map = new Map<string, string>();
	for (const p of policies) {
		const name = p.name?.trim();
		const desc = p.description?.trim();
		if (name && desc) {
			map.set(name, desc);
		}
	}
	return map;
}

export interface AwgmServicesSnapshot {
	usageLevel: string;
	interfaceWidth: string;
	theme: string;
	auth: string;
	logging: string;
	pingCheck: string;
	singbox: string;
	/** null — строка скрыта (ещё грузится). */
	hydraRoute: string | null;
	deviceProxy: string;
	dnsRoutes: string;
	awgTunnels: string;
	subscriptions: string;
}

function dash(v: string | number | boolean | null | undefined): string {
	if (v === null || v === undefined || v === '') return '—';
	return String(v);
}

/** NDMS hotspot: permit/none/пусто — политика по умолчанию; иначе описание (id). */
export function formatNdmsPolicyDisplay(
	policy: string | null | undefined,
	lookup?: PolicyNameLookup,
): string {
	const raw = (policy ?? '').trim();
	if (!raw) return 'По умолчанию (permit)';
	const key = raw.toLowerCase();
	if (key === 'permit') return 'По умолчанию (permit)';
	if (key === 'none') return 'По умолчанию (none)';

	const desc = lookup?.get(raw);
	if (desc) return `${desc} (${raw})`;
	return raw;
}

function resolveClientPolicyDisplay(ctx: RouterClientContext): string {
	const lookup = ctx.policyLookup;
	if (ctx.device) {
		return formatNdmsPolicyDisplay(ctx.device.policy, lookup);
	}
	const msg = (ctx.policyMessage ?? '').trim();
	if (!msg || msg === '—') return '—';
	if (/политику по умолчанию/i.test(msg)) return 'По умолчанию (permit)';
	const m = msg.match(/политику:\s*(.+)$/i);
	if (m) return formatNdmsPolicyDisplay(m[1].trim(), lookup);
	return '—';
}

function estimatePageZoom(): string {
	if (!browser || window.innerWidth <= 0) return '—';
	const ratio = window.outerWidth / window.innerWidth;
	if (!Number.isFinite(ratio) || ratio <= 0) return '—';
	return `~${Math.round(ratio * 100)}%`;
}

export function formatAwgmTheme(theme: ThemeState | null): string {
	return theme ? `${theme.label} (${theme.mode})` : '—';
}

/** Фактически применённая ширина (data-layout-compact на html). */
export function formatInterfaceWidth(): string {
	if (!browser) return '—';
	return document.documentElement.getAttribute('data-layout-compact') === 'true'
		? 'Компактная'
		: 'Классическая';
}

function connectionSummary(): string {
	if (!browser) return '—';
	const conn = (navigator as Navigator & { connection?: { effectiveType?: string; downlink?: number; rtt?: number } })
		.connection;
	if (!conn) return 'недоступно в этом браузере';
	const parts: string[] = [];
	if (conn.effectiveType) parts.push(conn.effectiveType);
	if (conn.downlink != null) parts.push(`${conn.downlink} Mbps`);
	if (conn.rtt != null) parts.push(`RTT ${conn.rtt} ms`);
	return parts.length ? parts.join(', ') : '—';
}

export function collectBrowserSnapshot(): BrowserSnapshot {
	if (!browser) {
		return {
			userAgent: '—',
			platform: '—',
			languages: '—',
			timezone: '—',
			screen: '—',
			viewport: '—',
			zoom: '—',
			devicePixelRatio: '—',
			colorDepth: '—',
			prefersColorScheme: '—',
			hardwareConcurrency: '—',
			onLine: '—',
			secureContext: '—',
			maxTouchPoints: '—',
			connection: '—',
			usageLevelAttr: '—',
			pageUrl: '—',
		};
	}

	const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
	const prefers = window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
	const root = document.documentElement;

	return {
		userAgent: navigator.userAgent,
		platform: navigator.platform || '—',
		languages: navigator.languages?.length ? navigator.languages.join(', ') : navigator.language,
		timezone: tz,
		screen: `${screen.width}×${screen.height} (доступно ${screen.availWidth}×${screen.availHeight})`,
		viewport: `${window.innerWidth}×${window.innerHeight}`,
		zoom: estimatePageZoom(),
		devicePixelRatio: String(window.devicePixelRatio),
		colorDepth: `${screen.colorDepth}-bit`,
		prefersColorScheme: prefers,
		hardwareConcurrency: navigator.hardwareConcurrency
			? String(navigator.hardwareConcurrency)
			: '—',
		onLine: navigator.onLine ? 'да' : 'нет',
		secureContext: window.isSecureContext ? 'да' : 'нет',
		maxTouchPoints: String(navigator.maxTouchPoints ?? 0),
		connection: connectionSummary(),
		usageLevelAttr: root.getAttribute('data-usage-level') ?? '—',
		pageUrl: location.href,
	};
}

export function browserSnapshotRows(s: BrowserSnapshot): AboutInfoRow[] {
	return [
		{ label: 'User-Agent', value: s.userAgent, title: s.userAgent },
		{ label: 'Платформа', value: s.platform },
		{ label: 'Языки', value: s.languages },
		{ label: 'Часовой пояс', value: s.timezone },
		{ label: 'Экран', value: s.screen },
		{ label: 'Окно (viewport)', value: s.viewport },
		{ label: 'Масштаб страницы', value: s.zoom },
		{ label: 'DPR', value: s.devicePixelRatio },
		{ label: 'Глубина цвета', value: s.colorDepth },
		{ label: 'Тема ОС', value: s.prefersColorScheme },
		{ label: 'Режим UI (атрибут)', value: s.usageLevelAttr },
		{ label: 'Сеть', value: s.connection },
		{ label: 'Ядра CPU', value: s.hardwareConcurrency },
		{ label: 'Онлайн', value: s.onLine },
		{ label: 'Secure context', value: s.secureContext },
		{ label: 'Touch points', value: s.maxTouchPoints },
		{ label: 'Страница', value: s.pageUrl, title: s.pageUrl },
	];
}

export function routerStaticRows(info: SystemInfo, level: UsageLevel): AboutInfoRow[] {
	const d = info.routerDetails;
	const model =
		d?.modelDisplay || d?.model || info.kernelModuleModel || '—';
	const region = d?.region?.trim();
	const modelLine = region ? `${model} (${region})` : model;
	const os =
		d?.firmwareRelease || info.firmwareVersion || info.keeneticOS || '—';
	const osLine = d?.portedBuild ? `${os} [Port]` : os;

	const rows: AboutInfoRow[] = [
		{ label: 'AWGM', value: info.version },
		{ label: 'Роутер', value: modelLine },
		{ label: 'KeeneticOS', value: `${osLine} (${info.isOS5 ? 'OS 5' : 'OS 4'})` },
		{ label: 'Backend AWG', value: info.activeBackend },
		{
			label: 'Модули AWG',
			value: `nativewg: ${info.backendAvailability?.nativewg ? 'да' : 'нет'}, kernel: ${info.backendAvailability?.kernel ? 'да' : 'нет'}`,
		},
	];

	if (info.singbox) {
		rows.push({
			label: 'Sing-box',
			value: info.singbox.installed
				? `установлен (v${info.singbox.version || '?'})`
				: 'не установлен',
		});
	}

	if (level !== 'basic') {
		rows.push(
			{ label: 'Архитектура Go', value: `${info.goOS}/${info.goArch}` },
			{
				label: 'Модуль ядра',
				value: info.kernelModuleLoaded
					? `загружен (${info.kernelModuleModel || '?'})`
					: info.kernelModuleExists
						? 'есть, не загружен'
						: 'нет',
			},
		);
	}

	if (level === 'expert' && d) {
		if (d.firmwareBuildDate) {
			rows.push({ label: 'Дата сборки', value: d.firmwareBuildDate });
		}
		if (d.firmwareSandbox) {
			rows.push({ label: 'Канал', value: d.firmwareSandbox });
		}
		if (d.vpnComponents?.length) {
			rows.push({ label: 'VPN (NDMS)', value: d.vpnComponents.join(' ') });
		}
		if (d.featureComponents?.length) {
			rows.push({ label: 'Features (NDMS)', value: d.featureComponents.join(' ') });
		}
	}

	return rows;
}

export function routerClientRows(ctx: RouterClientContext | null): AboutInfoRow[] {
	if (!ctx) {
		return [{ label: 'Статус', value: 'Не загружено' }];
	}

	const local =
		ctx.clientIP === '127.0.0.1' ||
		ctx.clientIP === '::1' ||
		ctx.clientIP === '';

	const rows: AboutInfoRow[] = [
		{ label: 'IP', value: dash(ctx.clientIP) },
		{ label: 'Hostname', value: dash(ctx.hostname) },
		{ label: 'Политика', value: resolveClientPolicyDisplay(ctx) },
	];

	if (local) {
		rows.push({
			label: 'Примечание',
			value: 'Запрос с роутера (localhost) — данные LAN-клиента могут отсутствовать',
		});
		return rows;
	}

	if (ctx.device) {
		rows.push(
			{ label: 'Имя в NDMS', value: dash(ctx.device.name || ctx.device.hostname) },
			{ label: 'MAC', value: dash(ctx.device.mac) },
			{ label: 'Связь', value: dash(ctx.device.link) },
			{ label: 'Активен', value: ctx.device.active ? 'да' : 'нет' },
		);
	} else if (ctx.fromRouter) {
		rows.push({
			label: 'NDMS hotspot',
			value: 'Устройство не найдено в списке клиентов',
		});
	}

	return rows;
}

export function buildRouterClientContext(
	dns: DnsCheckStartResponse | null,
	devices: PolicyDevice[] | null,
	policyLookup?: PolicyNameLookup,
): RouterClientContext | null {
	if (!dns) return null;

	const policyCheck = dns.checks.find((c) => c.id === 'client_policy');
	let device: PolicyDevice | null = null;

	if (devices && dns.clientIP) {
		device =
			devices.find((d) => d.ip === dns.clientIP) ??
			devices.find((d) => d.mac && dns.hostname && d.hostname === dns.hostname) ??
			null;
	}

	return {
		clientIP: dns.clientIP,
		hostname: dns.hostname,
		policyMessage: policyCheck?.message ?? '—',
		device,
		fromRouter: true,
		policyLookup,
	};
}

export function buildAwgmServicesSnapshot(input: {
	level: UsageLevel;
	theme: ThemeState | null;
	settings: Settings | null;
	authDisabled: boolean;
	authenticated: boolean;
	login: string | null;
	singbox: SingboxStatus | null;
	hydra: HydraRouteStatus | null;
	hydraLoaded?: boolean;
	showHydra?: boolean;
	deviceProxy: DeviceProxyConfig | null;
	deviceProxyRuntime: DeviceProxyRuntime | null;
	dnsRoutesTotal: number;
	dnsRoutesEnabled: number;
	dnsRoutesLoaded?: boolean;
	showDnsRoutes?: boolean;
	awgRunning: number;
	awgTotal: number;
	awgCountsLoaded?: boolean;
	subscriptionsEnabled: number;
	subscriptionsTotal: number;
	subscriptionsLoaded?: boolean;
}): AwgmServicesSnapshot {
	const level = input.level;

	let auth = '—';
	if (input.settings) {
		if (input.authDisabled) auth = 'отключена';
		else if (input.authenticated) auth = `вход: ${input.login || '?'}`;
		else auth = 'не авторизован';
	}

	let logging = '—';
	if (input.settings?.logging) {
		const l = input.settings.logging;
		logging = l.enabled
			? `вкл, ${l.logLevel}, app ${l.appMaxEntries}, sing-box ${l.singboxMaxEntries}`
			: 'выкл';
	}

	const ping = input.settings?.pingCheck?.enabled ? 'вкл' : 'выкл';

	let singbox = '—';
	if (input.singbox) {
		singbox = input.singbox.installed
			? input.singbox.running
				? `работает (v${input.singbox.version ?? input.singbox.currentVersion ?? '?'})`
				: 'остановлен'
			: 'не установлен';
	}

	let hydra: string | null = null;
	if (input.showHydra) {
		if (input.hydraLoaded) {
			if (input.hydra) {
				hydra = input.hydra.installed
					? input.hydra.running
						? 'работает'
						: 'остановлен'
					: 'не установлен';
			} else {
				hydra = '—';
			}
		}
	}

	let deviceProxy = '—';
	if (input.deviceProxy) {
		const cfg = input.deviceProxy;
		const rt = input.deviceProxyRuntime;
		deviceProxy = cfg.enabled
			? `вкл, порт ${cfg.port}, outbound: ${cfg.selectedOutbound || '—'}${rt?.activeTag ? `, активный: ${rt.activeTag}` : ''}`
			: 'выкл';
	}

	const dnsRoutes =
		!input.showDnsRoutes
			? '—'
			: input.dnsRoutesLoaded
				? `${input.dnsRoutesEnabled} вкл / ${input.dnsRoutesTotal} всего`
				: '…';

	const awgTunnels = input.awgCountsLoaded
		? `${input.awgRunning} запущено / ${input.awgTotal} всего`
		: '…';

	const subscriptions =
		level === 'basic'
			? 'раздел недоступен в базовом режиме'
			: input.subscriptionsLoaded
				? `${input.subscriptionsEnabled} вкл / ${input.subscriptionsTotal} всего`
				: '…';

	return {
		usageLevel: USAGE_LEVEL_LABELS[level],
		interfaceWidth: formatInterfaceWidth(),
		theme: formatAwgmTheme(input.theme),
		auth,
		logging,
		pingCheck: ping,
		singbox,
		hydraRoute: hydra,
		deviceProxy,
		dnsRoutes,
		awgTunnels,
		subscriptions,
	};
}

export function awgmServicesRows(s: AwgmServicesSnapshot): AboutInfoRow[] {
	const rows: AboutInfoRow[] = [
		{ label: 'Режим интерфейса', value: s.usageLevel },
		{ label: 'Ширина интерфейса', value: s.interfaceWidth },
		{ label: 'Тема AWGM', value: s.theme },
		{ label: 'Авторизация', value: s.auth },
		{ label: 'Журналирование', value: s.logging },
		{ label: 'Ping-check', value: s.pingCheck },
		{ label: 'Sing-box', value: s.singbox },
	];
	if (s.hydraRoute !== null) {
		rows.push({ label: 'HydraRoute Neo', value: s.hydraRoute });
	}
	rows.push(
		{ label: 'VPN для устройств', value: s.deviceProxy },
		{ label: 'DNS-маршруты', value: s.dnsRoutes },
		{ label: 'AWG-туннели', value: s.awgTunnels },
		{ label: 'SingBox подписки', value: s.subscriptions },
	);
	return rows;
}

export function formatAboutSection(title: string, rows: AboutInfoRow[]): string {
	const lines = [`## ${title}`];
	for (const row of rows) {
		lines.push(`${row.label}: ${row.value}`);
	}
	return lines.join('\n');
}

export function formatAboutReport(sections: { title: string; rows: AboutInfoRow[] }[]): string {
	const lines: string[] = ['AWG Manager — окружение', `Дата и время формирования: ${formatLocalTimestampWithOffset(new Date())}`, ''];

	for (const section of sections) {
		lines.push(`## ${section.title}`);
		for (const row of section.rows) {
			lines.push(`${row.label}: ${row.value}`);
		}
		lines.push('');
	}

	return lines.join('\n').trimEnd();
}

function formatLocalTimestampWithOffset(d: Date): string {
	const pad2 = (n: number): string => String(n).padStart(2, '0');
	const pad3 = (n: number): string => String(n).padStart(3, '0');

	const year = d.getFullYear();
	const month = pad2(d.getMonth() + 1);
	const day = pad2(d.getDate());
	const hours = pad2(d.getHours());
	const minutes = pad2(d.getMinutes());
	const seconds = pad2(d.getSeconds());
	const millis = pad3(d.getMilliseconds());

	const totalMinutes = -d.getTimezoneOffset();
	const sign = totalMinutes >= 0 ? '+' : '-';
	const absMinutes = Math.abs(totalMinutes);
	const tzHours = pad2(Math.floor(absMinutes / 60));
	const tzMinutes = pad2(absMinutes % 60);

	return `${year}-${month}-${day}T${hours}:${minutes}:${seconds}.${millis}${sign}${tzHours}:${tzMinutes}`;
}

export function formatLocalTimestampForFilename(d: Date): string {
	const pad2 = (n: number): string => String(n).padStart(2, '0');
	const year = d.getFullYear();
	const month = pad2(d.getMonth() + 1);
	const day = pad2(d.getDate());
	const hours = pad2(d.getHours());
	const minutes = pad2(d.getMinutes());
	const seconds = pad2(d.getSeconds());
	const totalMinutes = -d.getTimezoneOffset();
	const sign = totalMinutes >= 0 ? '+' : '-';
	const absMinutes = Math.abs(totalMinutes);
	const tzHours = pad2(Math.floor(absMinutes / 60));
	const tzMinutes = pad2(absMinutes % 60);
	return `${year}-${month}-${day}_${hours}-${minutes}-${seconds}${sign}${tzHours}-${tzMinutes}`;
}
