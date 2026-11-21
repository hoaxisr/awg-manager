import { m } from '$lib/i18n';
import type {
	FircStatus,
	FircTunnel,
	FircTunnelNodeState,
	FircTunnelState,
} from '$lib/types/firc';

export type FircDiagnosticTone = 'ok' | 'info' | 'warning' | 'error' | 'muted';

export interface FircTunnelDiagnostic {
	stateLabel: string;
	tone: FircDiagnosticTone;
	reason: string;
}

export function formatFircSkipReason(raw?: string | null): string {
	const trimmed = raw?.trim() ?? '';
	if (!trimmed) return '';
	if (trimmed === 'allowInsecure: needs --insecure') {
		return m.firc_skip_allow_insecure();
	}
	const nowhereMatch = trimmed.match(/^([^:]+):\s*nobody to answer$/);
	if (nowhereMatch) {
		return m.firc_skip_nowhere({ host: nowhereMatch[1].trim() });
	}
	if (trimmed === 'over the 256-node limit' || trimmed === 'more nodes than fit') {
		return m.firc_skip_over_cap();
	}
	if (trimmed === 'links glued without a separator') {
		return m.firc_skip_glued();
	}
	if (trimmed.startsWith('link longer than')) {
		return m.firc_skip_too_long();
	}
	if (trimmed === 'cannot parse the link' || trimmed === 'cannot be written as a link') {
		return m.firc_skip_unparseable();
	}
	if (trimmed === 'reality without pbk') {
		return m.firc_skip_no_pbk();
	}
	if (trimmed.startsWith('tls to an IP address without sni')) {
		return m.firc_skip_tls_ip_no_sni();
	}
	const trMatch = trimmed.match(/^transport (.+) is not supported$/);
	if (trMatch) {
		return m.firc_skip_unsupported_transport({ detail: trMatch[1].trim() });
	}
	const secMatch = trimmed.match(/^security=(.+) is not supported$/);
	if (secMatch) {
		return m.firc_skip_unsupported_security({ detail: secMatch[1].trim() });
	}
	const visMatch = trimmed.match(/^vision cannot run over (.+)$/);
	if (visMatch) {
		return m.firc_skip_vision_transport({ detail: visMatch[1].trim() });
	}
	if (trimmed === 'encryption is not supported') {
		return m.firc_skip_unsupported_encryption();
	}
	if (trimmed === 'tcp headerType=http is not supported') {
		return m.firc_skip_tcp_http();
	}
	if (
		trimmed === 'id is empty' ||
		trimmed === 'UUID with an invalid character' ||
		trimmed === 'id longer than a UUID' ||
		trimmed.includes('31 characters, need a UUID')
	) {
		return m.firc_skip_bad_uuid();
	}
	return trimmed;
}

export function formatFircSubError(raw?: string | null): string {
	const trimmed = raw?.trim() ?? '';
	if (!trimmed) return '';
	const placeholderMatch = trimmed.match(/^the server answered:\s*(.+)$/);
	if (placeholderMatch) {
		return m.firc_sub_err_placeholder({ said: placeholderMatch[1].trim() });
	}
	if (trimmed === 'no nodes in the answer') {
		return m.firc_sub_err_no_nodes();
	}
	const httpMatch = trimmed.match(/^HTTP\s+(\d+)$/);
	if (httpMatch) {
		return m.firc_sub_err_http({ code: httpMatch[1] });
	}
	if (trimmed === 'no answer') {
		return m.firc_sub_err_no_answer();
	}
	if (trimmed === 'the uplink mark could not be set') {
		return m.firc_sub_err_mark();
	}
	if (trimmed.startsWith('over ') && trimmed.includes('redirects')) {
		return m.firc_sub_err_limit();
	}
	return trimmed;
}

export function formatFircTunnelWhy(raw?: string | null): string {
	const trimmed = raw?.trim() ?? '';
	if (!trimmed) return '';
	if (trimmed === 'waiting for the router') {
		return m.firc_why_waiting_router();
	}
	return trimmed;
}

function mostCommonSkipReason(nodes: FircTunnelNodeState[]): string {
	const counts = new Map<string, number>();
	let bestRaw = '';
	let bestCount = 0;
	for (const node of nodes) {
		const raw = node.skipReason?.trim() ?? '';
		if (!raw) continue;
		const next = (counts.get(raw) ?? 0) + 1;
		counts.set(raw, next);
		if (next > bestCount) {
			bestCount = next;
			bestRaw = raw;
		}
	}
	return formatFircSkipReason(bestRaw);
}

export function getFircTunnelDiagnostic({
	tunnel,
	live,
	status,
}: {
	tunnel: FircTunnel;
	live?: FircTunnelState;
	status?: FircStatus | null;
}): FircTunnelDiagnostic {
	if (tunnel.enable === false) {
		return { stateLabel: m.firc_state_off(), tone: 'muted', reason: '' };
	}

	if (live?.status === 'up') {
		return { stateLabel: m.firc_state_running(), tone: 'ok', reason: '' };
	}

	if (!live) {
		if (status && !status.selected) {
			return {
				stateLabel: m.firc_state_not_selected(),
				tone: 'warning',
				reason: m.firc_reason_engine_not_selected(),
			};
		}
		if (status && status.recoveryRequired) {
			const recReason = status.recoveryReason?.trim() ?? '';
			return {
				stateLabel: m.firc_state_recovery(),
				tone: 'error',
				reason: recReason
					? m.firc_reason_recovery_detail({ reason: recReason })
					: m.firc_reason_recovery(),
			};
		}
		if (status && !status.running) {
			return {
				stateLabel: m.firc_state_daemon_stopped(),
				tone: 'error',
				reason: m.firc_reason_daemon_stopped(),
			};
		}
		return {
			stateLabel: m.firc_state_waiting(),
			tone: 'info',
			reason: m.firc_reason_state_loading(),
		};
	}

	if (live.status === 'waiting') {
		const subs = live.subscriptions ?? [];
		const errSub = subs.find((s) => Boolean(s.error?.trim()));
		if (errSub) {
			const formatted = formatFircSubError(errSub.error);
			const reason = subs.length > 1 && errSub.name ? `${errSub.name}: ${formatted}` : formatted;
			return {
				stateLabel: m.firc_state_sub_error(),
				tone: 'error',
				reason,
			};
		}
		if (subs.some((s) => s.fetching)) {
			return {
				stateLabel: m.firc_state_fetching(),
				tone: 'info',
				reason: m.firc_reason_sub_fetching(),
			};
		}
		const liveNodes = live.nodes ?? [];
		const skippedWithReason = liveNodes.filter(
			(n) => n.state === 'skipped' && Boolean(n.skipReason?.trim()),
		);
		if (
			liveNodes.length > 0 &&
			skippedWithReason.length > 0 &&
			liveNodes.every((n) => n.state === 'skipped' || n.state === 'excluded')
		) {
			const translated = mostCommonSkipReason(skippedWithReason);
			return {
				stateLabel: m.firc_state_nodes_skipped(),
				tone: 'error',
				reason: m.firc_reason_all_nodes_skipped({
					count: skippedWithReason.length,
					reason: translated,
				}),
			};
		}
		if (liveNodes.length > 0 && liveNodes.every((n) => n.state === 'excluded')) {
			return {
				stateLabel: m.firc_state_all_excluded(),
				tone: 'warning',
				reason: m.firc_reason_all_nodes_excluded(),
			};
		}
		if ((tunnel.sources ?? []).length === 0) {
			return {
				stateLabel: m.firc_state_no_sources(),
				tone: 'warning',
				reason: m.firc_reason_no_sources(),
			};
		}
		if (tunnel.filter?.trim() && liveNodes.length === 0) {
			return {
				stateLabel: m.firc_state_no_nodes(),
				tone: 'warning',
				reason: m.firc_reason_filter_empty({ filter: tunnel.filter.trim() }),
			};
		}
		return {
			stateLabel: m.firc_state_waiting(),
			tone: 'warning',
			reason: live.why?.trim() ? formatFircTunnelWhy(live.why) : m.firc_reason_waiting_nodes(),
		};
	}

	const whyReason = live.why?.trim() ? formatFircTunnelWhy(live.why) : '';
	switch (live.status) {
		case 'no_node':
			return {
				stateLabel: m.firc_state_no_node(),
				tone: 'error',
				reason: whyReason || m.firc_reason_no_node(),
			};
		case 'uplink_down':
			return {
				stateLabel: m.firc_state_uplink_down(),
				tone: 'error',
				reason: whyReason || m.firc_reason_uplink_down(),
			};
		case 'bad_config':
			return {
				stateLabel: m.firc_state_bad_config(),
				tone: 'error',
				reason: whyReason || m.firc_reason_bad_config(),
			};
		case 'backoff':
			return {
				stateLabel: m.firc_state_backoff(),
				tone: 'error',
				reason:
					whyReason ||
					(live.backoffS > 0
						? m.firc_reason_backoff_sec({ seconds: live.backoffS })
						: m.firc_reason_backoff()),
			};
		case 'starting':
			return {
				stateLabel: m.firc_state_starting(),
				tone: 'info',
				reason: whyReason || m.firc_reason_starting(),
			};
		case 'router':
			return {
				stateLabel: m.firc_state_router(),
				tone: 'warning',
				reason: whyReason || m.firc_reason_router(),
			};
		case 'off':
		default:
			return {
				stateLabel: m.firc_state_off(),
				tone: 'muted',
				reason: whyReason,
			};
	}
}
