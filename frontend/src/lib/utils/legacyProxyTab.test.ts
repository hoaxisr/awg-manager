import { describe, it, expect } from 'vitest';
import { legacyProxyTabRedirect } from './legacyProxyTab';

const at = (search: string) => new URL(`http://router.local/${search}`);

describe('legacyProxyTabRedirect', () => {
	it('уводит легаси ?tab=freeturn на вкладку «Выход»', () => {
		expect(legacyProxyTabRedirect(at('?tab=freeturn'))).toBe('/proxy?tab=exit');
	});

	it('уводит легаси ?tab=wdtt на вкладку «Выход»', () => {
		expect(legacyProxyTabRedirect(at('?tab=wdtt'))).toBe('/proxy?tab=exit');
	});

	it('бросает ?ft= вместе с легаси-вкладкой', () => {
		expect(legacyProxyTabRedirect(at('?tab=freeturn&ft=server'))).toBe('/proxy?tab=exit');
	});

	it('матч регистрозависимый — как у самих вкладок главной', () => {
		expect(legacyProxyTabRedirect(at('?tab=WDTT'))).toBeNull();
		expect(legacyProxyTabRedirect(at('?tab=FreeTurn'))).toBeNull();
	});

	it('не трогает живые вкладки главной', () => {
		expect(legacyProxyTabRedirect(at('?tab=singbox'))).toBeNull();
		expect(legacyProxyTabRedirect(at(''))).toBeNull();
	});
});
