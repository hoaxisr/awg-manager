import type { XrayClient } from './servers';

export interface XrayStatus {
	installed: boolean;
	running: boolean;
	configured?: boolean;
	backend?: string;
	binary_path?: string;
	unavailable_reason?: string;
	listen_address?: string;
	pid?: number;
	port?: number;
	version?: string;
	domain?: string;
	path?: string;
	uuid?: string;
	uplinkHTTPMethod?: string;
	cdnHost?: string;
	serverIP?: string;
	error?: string;
	clients_count?: number;
	clients?: XrayClient[];
	recovery_required?: boolean;
	recovery_reason?: string;
	recovery_fingerprint?: string;
}

export type VlessStatus = XrayStatus;

// ── Direct VLESS + REALITY entry (routers with a white IP) ──

export type XrayRealityTransport = 'tcp' | 'xhttp';

export interface XrayRealitySettings {
	enabled: boolean;
	port: number;
	transport: XrayRealityTransport;
	dest: string;
	server_names: string[] | null;
	public_key: string;
	short_ids: string[] | null;
	xhttp_path?: string;
	fingerprint: string;
	public_host?: string;
	updated_at?: string;
}

export interface XrayRealityUpdate {
	enabled?: boolean;
	port?: number;
	transport?: XrayRealityTransport;
	dest?: string;
	server_names?: string[];
	fingerprint?: string;
	public_host?: string;
	regenerate_keys?: boolean;
	reset?: boolean;
}

export interface XrayRealityNetwork {
	public_host: string;
	keendns_domain?: string;
	external_ip?: string;
	wan_address?: string;
	white_ip: boolean;
	reason?: string;
	checked_at?: string;
}

export interface XrayRealityRuntime {
	running: boolean;
	listen_addr?: string;
	transport?: string;
	active_conns: number;
	started_at?: string;
}

export interface XrayRealityState {
	settings: XrayRealitySettings;
	runtime: XrayRealityRuntime;
	last_error?: string;
	server_enabled: boolean;
	clients_count: number;
	wan_port_open: boolean;
	network?: XrayRealityNetwork | null;
	dest_presets: string[];
	port_candidates: number[];
	suggested_port: number;
}

export interface XrayRealityDestCheck {
	dest: string;
	ok: boolean;
	tls13: boolean;
	h2: boolean;
	x25519: boolean;
	latency_ms: number;
	server_name: string;
	error?: string;
}

export interface XrayRealityQuickSetupResponse {
	state: XrayRealityState;
	client?: { id: string; remark: string; enabled: boolean };
	links?: {
		vless_url: string;
		happ_json: string;
		singbox_json: string;
		mihomo_yaml: string;
		remark: string;
		uuid: string;
	};
	links_error?: string;
}
