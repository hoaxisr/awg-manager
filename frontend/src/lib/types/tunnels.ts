// ─────────────────────────────────────────────
// #region Tunnels — config, state, list items
// ─────────────────────────────────────────────

export interface AWGInterface {
	privateKey: string;
	address: string;
	mtu: number;
	dns?: string;
	jc: number;
	jmin: number;
	jmax: number;
	s1: number;
	s2: number;
	s3: number;
	s4: number;
	h1: string;
	h2: string;
	h3: string;
	h4: string;
	i1?: string;
	i2?: string;
	i3?: string;
	i4?: string;
	i5?: string;
	// AWG 3.0 device params (kernel mode only).
	headerProtectionKey?: string;
	contentPaddingAddition?: string;
	rekeyAfterTime?: string;
	rekeyTimeout?: string;
	rejectAfterTime?: string;
	keepaliveTimeout?: string;
	maxHandshakeAttempts?: string;
	// AWG 3.1 device flags (module 3.1+).
	randomTrailers?: boolean;
	disableCookies?: boolean;
}

export interface AWGPeer {
	publicKey: string;
	presharedKey?: string;
	endpoint: string;
	allowedIPs: string[];
	/** Секунды: число либо диапазон AWG 3.0 "min-max" строкой. */
	persistentKeepalive?: number | string;
}

export interface ConnectivityCheckConfig {
	method: 'http' | 'ping' | 'handshake' | 'disabled';
	pingTarget?: string;
}

export interface TunnelPingCheck {
	enabled: boolean;
	method: string;
	target: string;
	interval: number;
	deadInterval: number;
	failThreshold: number;
	minSuccess: number;
	timeout: number;
	port?: number;
	restart: boolean;
}

export interface TunnelStateInfo {
	state: number;
	opkgTunExists: boolean;
	interfaceUp: boolean;
	processRunning: boolean;
	processPID: number;
	hasPeer: boolean;
	hasHandshake: boolean;
	lastHandshake: string;
	rxBytes: number;
	txBytes: number;
	error: unknown;
	details?: string;
}

export interface AWGTunnel {
	id: string;
	name: string;
	type: string;
	enabled: boolean;
	defaultRoute: boolean;
	ispInterface?: string;
	ispInterfaceLabel?: string;
	interfaceName?: string;
	state?: string;
	stateInfo?: TunnelStateInfo;
	interface: AWGInterface;
	peer: AWGPeer;
	pingCheck?: TunnelPingCheck;
	connectivityCheck?: ConnectivityCheckConfig;
	warnings?: string[];
	backend?: 'nativewg' | 'kernel' | 'wdtt-raw';
	freeTurnClientId?: string;
	wdttClientId?: string;
}

export interface TunnelListItem {
	id: string;
	name: string;
	type: string;
	status: string;
	enabled: boolean;
	defaultRoute?: boolean;
	ispInterface?: string;
	ispInterfaceLabel?: string;
	resolvedIspInterface?: string;
	resolvedIspInterfaceLabel?: string;
	endpoint: string;
	address: string;
	interfaceName?: string;
	ndmsName?: string;
	hasAddressConflict?: boolean;
	rxBytes?: number;
	txBytes?: number;
	lastHandshake?: string;
	awgVersion?: 'wg' | 'awg1.0' | 'awg1.5' | 'awg2.0' | 'awg3' | 'awg3.1';
	mtu?: number;
	startedAt?: string;
	backend?: 'nativewg' | 'kernel' | 'wdtt-raw';
	connectivityCheck?: ConnectivityCheckConfig;
	/** id WDTT-клиента, для которого создан туннель (пусто у прочих). */
	wdttClientId?: string;
	/** То же для FreeTurn-клиента: пара к wdttClientId. */
	freeTurnClientId?: string;
	pingCheck: {
		status: 'alive' | 'recovering' | 'disabled';
		restartCount: number;
		failCount: number;
		failThreshold: number;
	};
	/** При true туннель защищён от изменений: выключить, изменить и удалить нельзя (#818). */
	locked?: boolean;
}

export interface DeleteResult {
	success: boolean;
	tunnelId: string;
	verified: boolean;
}

// #endregion

// ─────────────────────────────────────────────
// #region External & System Tunnels
// ─────────────────────────────────────────────

export interface ExternalTunnel {
	interfaceName: string;
	tunnelNumber: number;
	isAWG: boolean;
	publicKey?: string;
	endpoint?: string;
	lastHandshake?: string;
	rxBytes: number;
	txBytes: number;
}

export interface SystemTunnel {
	id: string;
	interfaceName: string;
	description: string;
	status: 'up' | 'down';
	connected: boolean;
	mtu: number;
	address?: string; // IPv4 e.g. "10.8.1.3"
	mask?: string;
	uptime?: number; // seconds since up
	peer?: {
		publicKey: string;
		endpoint: string;
		via?: string; // ISP/connection iface (e.g. "PPPoE0")
		rxBytes: number;
		txBytes: number;
		lastHandshake: string;
		online: boolean;
	};
}

export interface ASCParamsBase {
	jc: number;
	jmin: number;
	jmax: number;
	s1: number;
	s2: number;
	h1: string;
	h2: string;
	h3: string;
	h4: string;
}

export interface ASCParamsExtended extends ASCParamsBase {
	s3: number;
	s4: number;
	i1: string;
	i2: string;
	i3: string;
	i4: string;
	i5: string;
	// AWG 3.0 device params (kernel mode only). Optional: only present/edited
	// when the ASCEditor runs in awg3 mode.
	headerProtectionKey?: string;
	contentPaddingAddition?: string;
	rekeyAfterTime?: string;
	rekeyTimeout?: string;
	rejectAfterTime?: string;
	keepaliveTimeout?: string;
	maxHandshakeAttempts?: string;
	// AWG 3.1 device flags (kernel mode, module 3.1+). RandomTrailers is not
	// negotiated on the wire and has to be on at both ends.
	randomTrailers?: boolean;
	disableCookies?: boolean;
}

export type ASCParams = ASCParamsBase | ASCParamsExtended;

export interface SignatureCaptureResult {
	ok: boolean;
	source: string;
	packets: {
		i1: string;
		i2: string;
		i3: string;
		i4: string;
		i5: string;
	};
	warning?: string;
}

export interface SignatureGenerateResult {
	ok: boolean;
	source: string;
	protocol: string;
	byteSize: number;
	packets: {
		i1: string;
		i2: string;
		i3: string;
		i4: string;
		i5: string;
	};
}

// #endregion

// ─────────────────────────────────────────────
// #region PingCheck — status, logs, native config
// ─────────────────────────────────────────────

export interface NativePingCheckConfig {
	host: string;
	mode: 'icmp' | 'connect' | 'tls' | 'uri';
	updateInterval: number;
	maxFails: number;
	minSuccess: number;
	timeout: number;
	port?: number;
	restart: boolean;
}

export interface NativePingCheckStatus {
	exists: boolean;
	host: string;
	mode: string;
	interval: number;
	maxFails: number;
	minSuccess: number;
	timeout: number;
	port?: number;
	restart: boolean;
	bound: boolean;
	status: string;
	failCount: number;
	successCount: number;
}

export interface TunnelPingStatus {
	tunnelId: string;
	tunnelName: string;
	enabled: boolean;
	backend: 'kernel' | 'nativewg';
	status: 'alive' | 'recovering' | 'disabled' | 'stopped' | 'warming';
	method: string;
	lastCheck?: string;
	/** ms; -1 — проверка прошла, но время не измерено (см. latencyNote). */
	lastLatency: number;
	/** Почему время отклика не измерено; пусто — измеряется нормально. */
	latencyNote?: string;
	failCount: number;
	successCount?: number;
	failThreshold: number;
	restartCount: number;
	tunnelRunning?: boolean;
}

export interface PingLogEntry {
	timestamp: string;
	tunnelId: string;
	tunnelName: string;
	success: boolean;
	latency: number;
	error: string;
	failCount: number;
	threshold: number;
	stateChange: string;
	backend?: string;
}

// #endregion
