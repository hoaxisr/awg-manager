export interface UplinkProvider { id: string; device: string; label: string; up: boolean }
export interface WANDNSConfig { revision: number; overrides: Record<string, string[]> }
export interface UplinkPolicy {
	mode?: 'pinned' | 'fallback';
	failback?: 'manual' | 'automatic' | 'controlled';
	recoveryThreshold?: number;
	recoveryIntervalSeconds?: number;
	id: string;
	revision: number;
	target: { engine: string; ref: string; kind?: 'subscription' };
	providers: string[];
	enabled: boolean;
	intervalSeconds: number;
	timeoutSeconds: number;
	failureThreshold: number;
	holdDownSeconds: number;
}
export interface UplinkEntry {
	capabilities?: UplinkCapabilities;
	policy: UplinkPolicy;
	state: { phase: string; provider?: string; device?: string; lastError?: string; lastSuccess?: string; candidate?: string; recoverySuccesses?: number };
}
export interface UplinkCapabilities { providerDNS?: boolean; independentEngine?: boolean; independentProbe: boolean; automaticFailback: boolean; controlledFailback?: boolean; restartScope: string; limitation?: string }
export interface UplinkSnapshot { targetError?: string; targets?: { target: UplinkPolicy['target']; label: string; unavailableReason?: string; setupRequired?: boolean }[]; entries: UplinkEntry[]; providers: UplinkProvider[]; automaticFailback: boolean; capabilities?: Record<string,UplinkCapabilities> }
