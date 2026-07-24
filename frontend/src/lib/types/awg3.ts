/** Imported AWG3 (sing-box awg endpoint) tunnel — key-free API projection. */
export interface Awg3Tunnel {
	id: string;
	tag: string;
	host: string;
	headerProtection: boolean;
	/** AWG3 device timers — read-only passthrough (edited only in RouteBox).
	 * Strings as emitted (may be ranges like "120-150"); absent when unset. */
	rekeyTimeout?: string;
	rejectAfterTime?: string;
	keepaliveTimeout?: string;
	maxHandshakeAttempts?: string;
}
