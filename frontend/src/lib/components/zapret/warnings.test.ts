import { afterEach, expect, it } from 'vitest';
import { locale } from '$lib/i18n';
import { zapretWarningText } from './warnings';

const originalLocale = locale.current;
afterEach(() => locale.set(originalLocale));

it('localizes warning codes and keeps dynamic error details', () => {
	locale.set('en');
	expect(zapretWarningText({ code: 'hostlist_count_failed', detail: 'permission denied' })).toBe(
		'Could not count hostlist entries: permission denied'
	);

	locale.set('ru');
	expect(zapretWarningText({ code: 'hostlist_ip_only' })).toContain('В списке только IP-адреса и CIDR');
});

it('uses a localized fallback for an unknown future warning code', () => {
	locale.set('en');
	expect(zapretWarningText({ code: 'future_warning' })).toBe(
		'Zapret reported a warning that this version of AWGM does not recognize.'
	);
});
