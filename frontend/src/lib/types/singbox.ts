import type { Settings } from './system';

// #region Sing-box
// ─────────────────────────────────────────────

export interface SingboxTunnel {
	tag: string;
	protocol: 'vless' | 'hysteria2' | 'naive' | 'trojan' | 'shadowsocks' | 'mieru';
	server: string;
	port: number;
	security: 'reality' | 'tls' | 'none';
	transport: 'tcp' | 'grpc' | 'quic' | 'https';
	listenPort: number;
	proxyInterface: string;
	sni?: string;
	fingerprint?: string;
	username?: string;
	connectivity: {
		connected: boolean;
		latency: number | null;
	};
	kernelInterface?: string;
	/**
	 * True when sing-box process is alive AND the TUN interface (t2sX)
	 * exists in the kernel. Distinct from connectivity.connected, which
	 * reports latency health from the Clash API. Running=false with
	 * connected=true is impossible; running=true with connected=false
	 * means "process up, but outbound not reachable" (bad server, etc).
	 */
	running: boolean;
}

export interface SingboxStatus {
	installed: boolean;
	version?: string;
	running: boolean;
	pid?: number;
	tunnelCount: number;
	proxyComponent: boolean;
	/** Mirrors Settings.CreateNDMSProxyForSingbox. When false, the UI hides ProxyComponent warnings. */
	ndmsProxyEnabled: boolean;
	/**
	 * Build tags of the installed sing-box binary (parsed from
	 * `sing-box version` Tags: line). Missing when not installed.
	 * Example: ["with_gvisor","with_quic","with_naive_outbound"].
	 */
	features?: string[];
	/**
	 * Last fatal stderr message captured when sing-box exited (if any).
	 * Surfaced when `running === false` to explain why the daemon is
	 * down — typically a config-validation FATAL after a rule-set
	 * download failed.
	 */
	lastError?: string;
	/** Version of the currently installed sing-box binary. Missing when not installed. */
	currentVersion?: string;
	/** Minimum required sing-box version for full functionality. */
	requiredVersion: string;
	/** SHA256 of the currently installed sing-box binary. Missing when not installed. */
	currentSha256?: string;
	/** SHA256 of the sing-box binary pinned to this awg-manager build. */
	requiredSha256?: string;
	/** True when the installed sing-box version or SHA256 differs from the pinned binary. */
	updateAvailable: boolean;
	/**
	 * Классификация состояния installation: 'installed' | 'missing' |
	 * 'missing_no_space' | 'outdated_no_space' | 'installing' | 'error'.
	 */
	installState?: string;
	/** Требуемое свободное место (байты) для скачивания/обновления sing-box. */
	requiredBytes?: number;
	/** Свободное место (байты) на FS, где хранится managed binary. */
	freeBytes?: number;
}

export interface SingboxImportResponse {
	imported: SingboxTunnel[];
	errors: Array<{ line: number; input: string; error: string }>;
	tunnels: SingboxTunnel[]; // fresh full list
}

/**
 * Response envelope payload for GET /api/singbox/config-preview.
 * `json` is the pretty-printed merged sing-box config produced by
 * stitching all `01-*.json` fragments onto `00-base.json`.
 */
export interface SingboxConfigPreview {
	json: string;
}

/** Источник inbound'а merged-конфига (владелец-фича). */
export type SingboxInboundSource =
	| 'subscription'
	| 'group'
	| 'tunnel'
	| 'deviceproxy'
	| 'qos'
	| 'engine'
	| 'other';

/**
 * Один inbound merged-конфига sing-box (GET /api/singbox/inbounds):
 * нормализованная запись из per-slot чтения config.d с атрибуцией
 * источника и признаком «резерв порта» (idle).
 */
export interface SingboxInboundEntry {
	tag: string;
	/** mixed | tun | tproxy | redirect | socks | http | ... */
	type: string;
	/** Адрес прослушивания, например 127.0.0.1; пусто у tun. */
	listen: string;
	/** 0, когда listen_port отсутствует (tun). */
	listenPort: number;
	/** Слот оркестратора: base | tunnels | awg | qos-routes | router | fakeip | deviceproxy | subscriptions | ... */
	slot: string;
	source: SingboxInboundSource;
	/** Человекочитаемое имя владельца (метка подписки/группы, тег туннеля, имя инстанса device-proxy). */
	ownerLabel: string;
	/** true — резерв порта: inbound есть в конфиге, но его никто не питает. */
	idle: boolean;
	/**
	 * Причина idle (сигнал из самого конфига):
	 * no_route_rule — ни одно route-правило слота не направляет трафик с порта;
	 * ndms_proxy_disabled — тумблер «Создавать NDMS-прокси» выключен;
	 * ndms_proxy_missing — тумблер включён, но ProxyN не выделен.
	 */
	idleReason: '' | 'no_route_rule' | 'ndms_proxy_disabled' | 'ndms_proxy_missing';
}

/** Ответ GET /api/singbox/inbounds. warnings — нечитаемые слот-файлы и конфликты тегов inbound (fail-soft). */
export interface SingboxInboundsList {
	inbounds: SingboxInboundEntry[];
	warnings?: string[];
}

export interface SingboxTraffic {
	tag: string;
	upload: number;
	download: number;
}

export interface SingboxDelayEvent {
	tag: string;
	delay: number;
	timestamp: number;
}

// #endregion

// #region Monitoring (Phase 3)

export interface MonitoringTarget {
	id: string;
	host: string;
	name: string;
	url?: string;
}

export interface MonitoringTunnel {
	id: string;
	name: string;
	ifaceName: string;
	pingcheckTarget: string;
	selfTarget: string;
	selfMethod: string;
	/** "awg" | "system" | "singbox" — drives row visual hints. */
	source?: 'awg' | 'system' | 'singbox';
	/** AWG backend kind (for source==='awg'): "kernel" | "nativewg". */
	backend?: 'kernel' | 'nativewg' | 'system' | string;
	/** AWG protocol/version kind (for source==='awg'). */
	awgVersion?: 'wg' | 'awg1.0' | 'awg1.5' | 'awg2.0' | string;
	/** Managed AWG tunnel has "default route" enabled. */
	defaultRoute?: boolean;
	/** Sing-box row came from subscription member list. */
	subscription?: boolean;
	/** Optional sing-box metadata for badge rendering. */
	protocol?: string;
	security?: string;
	transport?: string;
	/** Sing-box outbound tag; empty unless source==='singbox'. */
	singboxTag?: string;
	/** Last Clash urltest delay in ms; 0 = no urltest data. */
	clashDelay?: number;
	/** urltest group tag this sing-box tunnel belongs to. */
	urltestGroup?: string;
}

export interface MonitoringCell {
	targetId: string;
	tunnelId: string;
	latencyMs: number | null;
	ok: boolean;
	activeForRestart: boolean;
	isSelf: boolean;
	ts: string;
}

export interface MonitoringSnapshot {
	targets: MonitoringTarget[];
	tunnels: MonitoringTunnel[];
	cells: MonitoringCell[];
	updatedAt: string;
}

// #endregion
