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
import { usageLevelLabel } from '$lib/types/usageLevel';
import type { ThemeState } from '$lib/stores/theme';
import { m } from '$lib/i18n';

export interface AboutInfoRow {
	/** Стабильный ключ строки — логика сравнивает его, а не переведённый `label`. */
	id: string;
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
	clientRoutes: string;
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
	if (!raw) return m.about_device_policy_default_permit();
	const key = raw.toLowerCase();
	if (key === 'permit') return m.about_device_policy_default_permit();
	if (key === 'none') return m.about_device_policy_default_none();

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
	if (/политику по умолчанию/i.test(msg)) return m.about_device_policy_default_permit();
	const match = msg.match(/политику:\s*(.+)$/i);
	if (match) return formatNdmsPolicyDisplay(match[1].trim(), lookup);
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
		? m.about_device_width_compact()
		: m.about_device_width_classic();
}

function connectionSummary(): string {
	if (!browser) return '—';
	const conn = (navigator as Navigator & { connection?: { effectiveType?: string; downlink?: number; rtt?: number } })
		.connection;
	if (!conn) return m.about_device_value_connection_unavailable();
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
		screen: m.about_device_value_screen({
			width: screen.width,
			height: screen.height,
			availWidth: screen.availWidth,
			availHeight: screen.availHeight,
		}),
		viewport: `${window.innerWidth}×${window.innerHeight}`,
		zoom: estimatePageZoom(),
		devicePixelRatio: String(window.devicePixelRatio),
		colorDepth: `${screen.colorDepth}-bit`,
		prefersColorScheme: prefers,
		hardwareConcurrency: navigator.hardwareConcurrency
			? String(navigator.hardwareConcurrency)
			: '—',
		onLine: navigator.onLine ? m.about_device_value_yes() : m.about_device_value_no(),
		secureContext: window.isSecureContext ? m.about_device_value_yes() : m.about_device_value_no(),
		maxTouchPoints: String(navigator.maxTouchPoints ?? 0),
		connection: connectionSummary(),
		usageLevelAttr: root.getAttribute('data-usage-level') ?? '—',
		pageUrl: location.href,
	};
}

export function browserSnapshotRows(s: BrowserSnapshot): AboutInfoRow[] {
	return [
		{ id: 'userAgent', label: 'User-Agent', value: s.userAgent, title: s.userAgent },
		{ id: 'platform', label: m.about_device_label_platform(), value: s.platform },
		{ id: 'languages', label: m.about_device_label_languages(), value: s.languages },
		{ id: 'timezone', label: m.about_device_label_timezone(), value: s.timezone },
		{ id: 'screen', label: m.about_device_label_screen(), value: s.screen },
		{ id: 'viewport', label: m.about_device_label_viewport(), value: s.viewport },
		{ id: 'zoom', label: m.about_device_label_zoom(), value: s.zoom },
		{ id: 'dpr', label: 'DPR', value: s.devicePixelRatio },
		{ id: 'colorDepth', label: m.about_device_label_color_depth(), value: s.colorDepth },
		{ id: 'osTheme', label: m.about_device_label_os_theme(), value: s.prefersColorScheme },
		{ id: 'usageLevelAttr', label: m.about_device_label_ui_mode_attr(), value: s.usageLevelAttr },
		{ id: 'network', label: m.about_device_label_network(), value: s.connection },
		{ id: 'cpuCores', label: m.about_device_label_cpu_cores(), value: s.hardwareConcurrency },
		{ id: 'online', label: m.about_device_label_online(), value: s.onLine },
		{ id: 'secureContext', label: 'Secure context', value: s.secureContext },
		{ id: 'touchPoints', label: 'Touch points', value: s.maxTouchPoints },
		{ id: 'page', label: m.about_device_label_page(), value: s.pageUrl, title: s.pageUrl },
	];}

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
		{ id: 'awgm', label: 'AWGM', value: info.version },
		{ id: 'router', label: m.diag_about_section_router(), value: modelLine },
		{ id: 'keeneticOS', label: 'KeeneticOS', value: `${osLine} (${info.isOS5 ? 'OS 5' : 'OS 4'})` },
		{ id: 'backendAwg', label: 'Backend AWG', value: info.activeBackend },
		{
			id: 'awgModules',
			label: m.about_device_label_awg_modules(),
			value: m.about_device_value_awg_modules({
				nativewg: info.backendAvailability?.nativewg ? m.about_device_value_yes() : m.about_device_value_no(),
				kernel: info.backendAvailability?.kernel ? m.about_device_value_yes() : m.about_device_value_no(),
			}),
		},
	];

	if (info.singbox) {
		rows.push({
			id: 'singbox',
			label: 'Sing-box',
			value: info.singbox.installed
				? m.about_device_value_singbox_installed({ version: info.singbox.version || '?' })
				: m.about_device_value_not_installed(),
		});
	}

	if (level !== 'basic') {
		rows.push(
			{ id: 'goArch', label: m.about_device_label_go_arch(), value: `${info.goOS}/${info.goArch}` },
			{
				id: 'kernelModule',
				label: m.about_device_label_kernel_module(),
				value: info.kernelModuleLoaded
					? m.about_device_value_kernel_loaded({ model: info.kernelModuleModel || '?' })
					: info.kernelModuleExists
						? m.about_device_value_kernel_present_not_loaded()
						: m.about_device_value_no(),
			},
		);
	}

	if (level === 'expert' && d) {
		if (d.firmwareBuildDate) {
			rows.push({ id: 'buildDate', label: m.about_device_label_build_date(), value: d.firmwareBuildDate });
		}
		if (d.firmwareSandbox) {
			rows.push({ id: 'channel', label: m.about_device_label_channel(), value: d.firmwareSandbox });
		}
		if (d.vpnComponents?.length) {
			rows.push({ id: 'vpnComponents', label: 'VPN (NDMS)', value: d.vpnComponents.join(' ') });
		}
		if (d.featureComponents?.length) {
			rows.push({ id: 'featureComponents', label: 'Features (NDMS)', value: d.featureComponents.join(' ') });
		}
	}

	return rows;
}

export function routerClientRows(ctx: RouterClientContext | null): AboutInfoRow[] {
	if (!ctx) {
		return [{ id: 'status', label: m.about_device_label_status(), value: m.about_device_value_not_loaded() }];
	}

	const local =
		ctx.clientIP === '127.0.0.1' ||
		ctx.clientIP === '::1' ||
		ctx.clientIP === '';

	const rows: AboutInfoRow[] = [
		{ id: 'ip', label: 'IP', value: dash(ctx.clientIP) },
		{ id: 'hostname', label: 'Hostname', value: dash(ctx.hostname) },
		{ id: 'policy', label: m.about_device_label_policy(), value: resolveClientPolicyDisplay(ctx) },
	];

	if (local) {
		rows.push({
			id: 'note',
			label: m.about_device_label_note(),
			value: m.about_device_value_localhost_note(),
		});
		return rows;
	}

	if (ctx.device) {
		rows.push(
			{ id: 'ndmsName', label: m.about_device_label_ndms_name(), value: dash(ctx.device.name || ctx.device.hostname) },
			{ id: 'mac', label: 'MAC', value: dash(ctx.device.mac) },
			{ id: 'link', label: m.about_device_label_link(), value: dash(ctx.device.link) },
			{ id: 'active', label: m.about_device_label_active(), value: ctx.device.active ? m.about_device_value_yes() : m.about_device_value_no() },
		);
	} else if (ctx.fromRouter) {
		rows.push({
			id: 'ndmsHotspot',
			label: 'NDMS hotspot',
			value: m.about_device_value_device_not_found(),
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
	clientRoutesTotal: number;
	clientRoutesEnabled: number;
	clientRoutesLoaded?: boolean;
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
		if (input.authDisabled) auth = m.about_device_value_auth_disabled();
		else if (input.authenticated) auth = m.about_device_value_auth_login({ login: input.login || '?' });
		else auth = m.about_device_value_auth_none();
	}

	let logging = '—';
	if (input.settings?.logging) {
		const l = input.settings.logging;
		logging = l.enabled
			? m.about_device_value_logging_on({
					level: l.logLevel,
					app: l.appMaxEntries,
					singbox: l.singboxMaxEntries,
				})
			: m.about_device_value_off();
	}

	const ping = input.settings?.pingCheck?.enabled ? m.about_device_value_on() : m.about_device_value_off();

	let singbox = '—';
	if (input.singbox) {
		singbox = input.singbox.installed
			? input.singbox.running
				? m.about_device_value_running_version({
						version: input.singbox.version ?? input.singbox.currentVersion ?? '?',
					})
				: m.about_device_value_stopped()
			: m.about_device_value_not_installed();
	}

	let hydra: string | null = null;
	if (input.showHydra) {
		if (input.hydraLoaded) {
			if (input.hydra) {
				hydra = input.hydra.installed
					? input.hydra.running
						? m.about_device_value_running()
						: m.about_device_value_stopped()
					: m.about_device_value_not_installed();
			} else {
				hydra = '—';
			}
		}
	}

	let deviceProxy = '—';
	if (input.deviceProxy) {
		const cfg = input.deviceProxy;
		const rt = input.deviceProxyRuntime;
		const outbound = cfg.selectedOutbound || '—';
		deviceProxy = cfg.enabled
			? rt?.activeTag
				? m.about_device_value_proxy_on_active({ port: cfg.port, outbound, active: rt.activeTag })
				: m.about_device_value_proxy_on({ port: cfg.port, outbound })
			: m.about_device_value_off();
	}

	const clientRoutes = input.clientRoutesLoaded
		? m.about_device_value_counts({ enabled: input.clientRoutesEnabled, total: input.clientRoutesTotal })
		: '…';

	const dnsRoutes =
		!input.showDnsRoutes
			? '—'
			: input.dnsRoutesLoaded
				? m.about_device_value_counts({ enabled: input.dnsRoutesEnabled, total: input.dnsRoutesTotal })
				: '…';

	const awgTunnels = input.awgCountsLoaded
		? m.about_device_value_awg_counts({ running: input.awgRunning, total: input.awgTotal })
		: '…';

	const subscriptions =
		level === 'basic'
			? m.about_device_value_subscriptions_unavailable_basic()
			: input.subscriptionsLoaded
				? m.about_device_value_counts({ enabled: input.subscriptionsEnabled, total: input.subscriptionsTotal })
				: '…';

	return {
		usageLevel: usageLevelLabel(level),
		interfaceWidth: formatInterfaceWidth(),
		theme: formatAwgmTheme(input.theme),
		auth,
		logging,
		pingCheck: ping,
		singbox,
		hydraRoute: hydra,
		deviceProxy,
		clientRoutes,
		dnsRoutes,
		awgTunnels,
		subscriptions,
	};
}

export function awgmServicesRows(s: AwgmServicesSnapshot): AboutInfoRow[] {
	const rows: AboutInfoRow[] = [
		{ id: 'usageLevel', label: m.about_device_label_ui_mode(), value: s.usageLevel },
		{ id: 'interfaceWidth', label: m.about_device_label_ui_width(), value: s.interfaceWidth },
		{ id: 'theme', label: m.about_device_label_theme(), value: s.theme },
		{ id: 'auth', label: m.about_device_label_auth(), value: s.auth },
		{ id: 'logging', label: m.about_device_label_logging(), value: s.logging },
		{ id: 'pingCheck', label: 'Ping-check', value: s.pingCheck },
		{ id: 'singbox', label: 'Sing-box', value: s.singbox },
	];
	if (s.hydraRoute !== null) {
		rows.push({ id: 'hydraRoute', label: 'HydraRoute Neo', value: s.hydraRoute });
	}
	rows.push(
		// Прокси (SOCKS5/HTTP) и «VPN для устройств» (client routes) — разные
		// подсистемы; до #663 прокси показывался под именем вкладки client routes.
		{ id: 'deviceProxy', label: m.about_device_label_device_proxy(), value: s.deviceProxy },
		{ id: 'clientRoutes', label: m.about_device_label_device_vpn(), value: s.clientRoutes },
		{ id: 'dnsRoutes', label: m.about_device_label_dns_routes(), value: s.dnsRoutes },
		{ id: 'awgTunnels', label: m.about_device_label_awg_tunnels(), value: s.awgTunnels },
		{ id: 'subscriptions', label: m.about_device_label_singbox_subs(), value: s.subscriptions },
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
	const lines: string[] = [
		m.about_device_report_heading(),
		m.about_device_report_generated_at({ timestamp: formatLocalTimestampWithOffset(new Date()) }),
		'',
	];

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
