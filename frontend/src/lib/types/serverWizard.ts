export type WizardKind = 'tgwebproxy' | 'xray' | 'both';

export type CheckStatus = 'ok' | 'warning' | 'blocked';

export type VerificationCheckStatus = CheckStatus | 'not_applicable';

export interface PreflightCheck {
	id: string;
	title: string;
	status: CheckStatus;
	message: string;
	details?: string;
	remediation?: string;
}

export interface PreflightResponse {
	can_proceed: boolean;
	has_warnings: boolean;
	checks: PreflightCheck[];
	fingerprint: string;
}

export interface CDNProfile {
	id: string;
	name: string;
	description: string;
	capabilities: string[];
	instructions: string;
	recommended: boolean;
}

export type KeenDNSState =
	| 'ready'
	| 'cert_pending'
	| 'conflict'
	| 'unconfigured'
	| 'unsupported'
	| 'error';

export interface KeenDNSPublication {
	label: string;
	fqdn: string;
	upstream: string;
	access?: string;
	security?: string;
	static?: boolean;
	compatible: boolean;
	conflict_reason?: string;
}

export interface KeenDNSDiscovery {
	state: KeenDNSState;
	enabled: boolean;
	domain: string;
	mode: 'direct' | 'cloud' | 'unknown' | string;
	cert_ok: boolean;
	cert_info?: string;
	error_detail?: string;
	publications: KeenDNSPublication[];
	wan_ip?: string;
	wan_ip_private: boolean;
}

export interface EgressOption {
	id: string;
	name: string;
	iface?: string;
	kind: 'direct' | 'interface' | 'socks' | 'router_outbound';
	owner: string;
	supported_services?: ('xray' | 'tgwebproxy')[];
	state?: 'available' | 'degraded' | 'unavailable' | 'unsupported';
	available: boolean;
	degraded_msg?: string;
	unavailable_reason?: string;
	current?: boolean;
	supports_tcp: boolean;
	supports_udp: boolean;
	generation: number;
}

export interface CapabilitiesResponse {
	kind: WizardKind;
	scenarios?: string[];
	modes?: string[];
	profiles: CDNProfile[];
	egress_options: EgressOption[];
	keendns?: KeenDNSDiscovery;
	configured: boolean;
	running: boolean;
	recovery_required: boolean;
	state_fingerprint?: string;
}

export interface PlanItem {
	action: string;
	target: string;
	description: string;
	old_value?: string;
	new_value?: string;
}

export interface ChangePlan {
	summary: string;
	items: PlanItem[];
	restart_required: boolean;
	state_fingerprint: string;
}

export interface WizardPlanRequest {
	kind: WizardKind;
	scenario?: string;
	device_type?: string;
	mode?: string;
	public_domain?: string;
	tg_public_domain?: string;
	public_port?: number;
	path?: string;
	dispatcher_port?: number;
	listen_port?: number;
	tg_listen_port?: number;
	direct_host?: string;
	direct_port?: number;
	tls_domain?: string;
	upstream_device?: string;
	tg_upstream_device?: string;
	client_remark?: string;
	cdn_profile_id?: string;
	carrier_mode?: string;
	connection_way?: 'direct' | 'external_https';
	origin_type?: 'public_address' | 'cloud_address';
	origin_host?: string;
	origin_scheme?: 'http' | 'https';
	origin_port?: number;
	preserve_transport?: boolean;
	raw_transport?: string;
	raw_mode?: string;
	raw_uplink_method?: string;
	preserve_existing_credentials?: boolean;
	existing_client_uuid?: string;
	keendns_mode?: 'manual' | 'existing' | 'create';
	keendns_label?: string;
	disable_other_service?: boolean;
}

export interface ServerPlanRecord {
	plan_id: string;
	kind: WizardKind;
	session_id?: string;
	request: WizardPlanRequest;
	plan: ChangePlan;
	state_fingerprint: string;
	expires_at: string;
	used: boolean;
	created_at?: string;
}

export type JobPhase =
	| 'pending'
	| 'preparing'
	| 'applying'
	| 'verifying'
	| 'committing'
	| 'succeeded'
	| 'cancelling'
	| 'rolling_back'
	| 'cancelled'
	| 'failed'
	| 'recovery_required';

export interface JobStatusResponse {
	id: string;
	kind: WizardKind;
	phase: JobPhase;
	progress: number;
	current_step: string;
	error?: string;
	error_code?: string;
	result_available: boolean;
	created_at: string;
	updated_at: string;
}

export interface RevealCredentials {
	kind: WizardKind;
	vless_url?: string;
	happ_json?: string;
	singbox_json?: string;
	mihomo_yaml?: string;
	uuid?: string;
	remark?: string;
	direct_link?: string;
	web_link?: string;
	tg_secret?: string;
	/** Секрет прямого MTProxy для ручного ввода (ee + секрет + домен Fake-TLS). */
	mtproxy_secret?: string;
	direct_host?: string;
	direct_port?: number;
	public_host?: string;
	tg_public_host?: string;
	xray_egress?: string;
	tg_egress?: string;
	origin_host?: string;
	origin_scheme?: string;
	origin_port?: number;
	keendns_fqdn?: string;
	keendns_cert_status?: string;
}

export type VerificationErrorCategory =
	| 'dns'
	| 'tls'
	| 'http_origin'
	| 'auth'
	| 'egress'
	| 'timeout'
	| 'firewall';

export interface VerificationCheck {
	id: string;
	service: 'xray' | 'tg_mtproto' | 'tg_web' | 'dispatcher' | 'egress';
	title: string;
	status: VerificationCheckStatus;
	category?: VerificationErrorCategory;
	message: string;
	remediation?: string;
	target_address?: string;
}

export interface VerificationReport {
	kind: WizardKind;
	checked_at: string;
	state_fingerprint: string;
	scope: string;
	scope_note: string;
	overall_status: 'ok' | 'warning' | 'blocked';
	checks: VerificationCheck[];
}
