import { describe, expect, it } from 'vitest';
import { describeRouterReference } from './tunnelRefs';

describe('describeRouterReference', () => {
	it('describes route.final', () => {
		expect(describeRouterReference('route.final')).toEqual({
			text: 'Назначен маршрутом по умолчанию',
			known: true
		});
	});

	it('describes a composite member', () => {
		expect(describeRouterReference('outbounds[0="vpn-default"].outbounds[3]')).toEqual({
			text: 'Входит в группу маршрутов «vpn-default»',
			known: true
		});
	});

	it('describes a composite default', () => {
		expect(describeRouterReference('outbounds[2="grp"].default')).toEqual({
			text: 'Выбран по умолчанию в группе «grp»',
			known: true
		});
	});

	it('describes a dns detour', () => {
		expect(describeRouterReference('dns.servers[1="my-dns"].detour')).toEqual({
			text: 'Используется DNS-сервером «my-dns»',
			known: true
		});
	});

	it('describes a rule_set download_detour', () => {
		expect(describeRouterReference('route.rule_set[0="geoip-ru"].download_detour')).toEqual({
			text: 'Через него скачивается список «geoip-ru»',
			known: true
		});
	});

	it('handles names with special characters', () => {
		expect(describeRouterReference('outbounds[0="vpn [eu] #1"].outbounds[0]')).toEqual({
			text: 'Входит в группу маршрутов «vpn [eu] #1»',
			known: true
		});
	});

	it('falls back to the raw path for unknown formats', () => {
		expect(describeRouterReference('something.unexpected[5]')).toEqual({
			text: 'something.unexpected[5]',
			known: false
		});
	});
});
