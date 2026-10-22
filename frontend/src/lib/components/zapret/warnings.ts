import { m } from '$lib/i18n';
import type { ZapretWarning } from '$lib/types';

/** Convert API warning codes to text in the user's selected language. */
export function zapretWarningText(warning: ZapretWarning): string {
	switch (warning.code) {
		case 'hostlist_count_failed':
			return m.zapret_warning_hostlist_count_failed({ detail: warning.detail ?? '' });
		case 'hostlist_ip_only':
			return m.zapret_warning_hostlist_ip_only();
		case 'circular_missing_key':
			return m.zapret_warning_circular_missing_key();
		case 'strategy_no_lua_desync':
			return m.zapret_warning_strategy_no_lua_desync();
		case 'strategies_empty':
			return m.zapret_warning_strategies_empty({ optVar: warning.detail ?? 'NFQWS2_OPT' });
		case 'strategies_limit_reached':
			return m.zapret_warning_strategies_limit_reached({ limit: warning.detail ?? '64' });
		default:
			return m.zapret_warning_unknown();
	}
}
