import type { AutolistConfig } from '$lib/types';

export function buildAutolistConfigPayload(cfg: AutolistConfig): AutolistConfig {
	return {
		autoAdd: cfg.autoAdd,
		targetInterface: cfg.targetInterface?.trim() ?? '',
		targetOutbound: cfg.targetOutbound?.trim() ?? '',
		resolveVia: cfg.resolveVia,
		resolveIPEnabled: cfg.resolveIPEnabled,
		probeInterface: cfg.probeInterface ?? '',
		verbose: cfg.verbose ?? false,
		packetEnabled: cfg.packetEnabled ?? false,
		instant: cfg.instant ?? false,
		tunnelProbe: cfg.tunnelProbe ?? false,
		wanCheckUrl: cfg.wanCheckUrl?.trim() ?? '',
		excludeDomains: cfg.excludeDomains ?? [],
		excludeRuleSets: cfg.excludeRuleSets ?? [],
	};
}
