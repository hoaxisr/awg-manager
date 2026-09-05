// ─────────────────────────────────────────────
// #region Servers — WireGuard, managed server
// ─────────────────────────────────────────────

export interface WireguardServer {
	id: string;
	interfaceName: string;
	description: string;
	status: 'up' | 'down';
	connected: boolean;
	mtu: number;
	address: string;
	mask: string;
	publicKey: string;
	listenPort: number;
	peers: WireguardServerPeer[];
	natEnabled?: boolean;
	natMode?: 'full' | 'internet-only' | 'none';
	policy?: string;
	keenDnsDomain?: string;
	/** User-configured connect host for client .conf; empty = WAN IP at generation. */
	endpoint?: string;
	builtIn?: boolean;
	/**
	 * False when the backend failed to read NAT mode / policy from NDMS
	 * (e.g. transient router error). When false, natMode/policy are NOT
	 * trustworthy and the UI must show an "unknown" state rather than the
	 * zero-valued 'none'. Absent (legacy/managed) is treated as known.
	 */
	natModeKnown?: boolean;
	policyKnown?: boolean;
	/** NDMS admin intent (conf layer running). Prefer over status/connected for toggles. */
	enabled?: boolean;
	enabledKnown?: boolean;
}

export interface WireguardServerPeer {
	publicKey: string;
	description: string;
	endpoint: string;
	allowedIPs?: string[];
	rxBytes: number;
	txBytes: number;
	lastHandshake: string;
	online: boolean;
	enabled: boolean;
	confAvailable?: boolean;
}

export interface WireguardServerConfig {
	publicKey: string;
	listenPort: number;
	mtu: number;
	address: string;
	peers: WireguardServerPeerConfig[];
}

export interface WireguardServerPeerConfig {
	publicKey: string;
	description: string;
	presharedKey: string;
	allowedIPs: string[];
	address: string;
}

export interface ManagedServer {
	interfaceName: string;
	description?: string;
	address: string;
	mask: string;
	listenPort: number;
	endpoint?: string;
	dns?: string;
	mtu?: number;
	natEnabled?: boolean;
	natMode?: 'full' | 'internet-only' | 'none';
	lanSegments?: string[];
	foreignAcls?: string[];
	policy: string;
	peers: ManagedPeer[];
}

export interface ManagedPeer {
	publicKey: string;
	privateKey: string;
	presharedKey: string;
	description: string;
	tunnelIP: string;
	dns?: string;
	enabled: boolean;
}

export interface ManagedServerStats {
	status: string;
	peers: ManagedPeerStats[];
}

export interface ManagedPeerStats {
	publicKey: string;
	endpoint: string;
	rxBytes: number;
	txBytes: number;
	lastHandshake: string;
	online: boolean;
}

export interface CreateManagedServerRequest {
	address: string;
	mask: string;
	listenPort: number;
	description?: string;
	endpoint?: string;
	dns?: string;
	mtu?: number;
	generateAsc?: boolean;
}

// UpdateManagedServerRequest matches the Go-side pointer-field semantics:
// - omit a field entirely (do not include it in the body) to PRESERVE the existing value
// - include a field (even with empty string / 0) to SET it (empty string CLEARS)
// Build the payload conditionally on the call site so a value the user
// didn't touch never appears in the request.
export interface UpdateManagedServerRequest {
	address: string;
	mask: string;
	listenPort: number;
	description?: string;
	endpoint?: string;
	dns?: string;
	mtu?: number;
}

export interface AddManagedPeerRequest {
	description: string;
	tunnelIP: string;
	dns?: string;
}

export interface UpdateManagedPeerRequest {
	description: string;
	tunnelIP: string;
	dns?: string;
}

// #endregion

// #region Managed Server Backup / Restore
// ─────────────────────────────────────────────

/**
 * Single managed server entry as exported to a backup file.
 * Shape mirrors storage.ManagedServer JSON; policy is optional
 * because newly-created servers may not have one assigned yet.
 */
export interface ManagedServerExport {
	interfaceName: string;
	description?: string;
	address: string;
	mask: string;
	listenPort: number;
	endpoint?: string;
	dns?: string;
	mtu?: number;
	natEnabled?: boolean;
	policy?: string;
	privateKey?: string;
	i1?: string;
	i2?: string;
	i3?: string;
	i4?: string;
	i5?: string;
	peers: ManagedPeer[];
}

export interface ManagedServerBackupFile {
	version: number;
	type: string;
	exportedAt: string;
	managedServers: ManagedServerExport[];
	warnings?: Array<{
		interfaceName?: string;
		message: string;
	}>;
}

export interface RestoreOptions {
	allowRenumber: boolean;
}

export interface RestoreOutcome {
	name: string;
	newName?: string;
	action: 'created' | 'merged' | 'renamed' | 'conflict' | 'failed';
	addedPeers?: number;
	conflicts?: string[];
	error?: string;
}

export interface ManagedServerRestoreResponse {
	outcomes: RestoreOutcome[];
}

export interface ManagedServerDriftResponse {
	drift: ManagedServerExport[];
}

// #endregion
