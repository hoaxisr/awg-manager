import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/svelte';
import DNSRuleEditModal from './DNSRuleEditModal.svelte';
import type { SingboxRouterDNSRule, SingboxRouterDNSServer, SingboxRouterRuleSet } from '$lib/types';

class ResizeObserverStub {
	observe(): void {}
	disconnect(): void {}
}
vi.stubGlobal('ResizeObserver', ResizeObserverStub);

const servers: SingboxRouterDNSServer[] = [{ tag: 'dns-direct', type: 'udp', server: '1.1.1.1' }];

const baseProps = {
	servers,
	availableRuleSets: [],
	onClose: vi.fn(),
};

const evalRD: SingboxRouterDNSRule = { action: 'evaluate', server: 'dns-direct', tag: 'rd' };
const evalAnon: SingboxRouterDNSRule = { action: 'evaluate', server: 'dns-direct' };

/** Открывает Dropdown по видимому тексту его триггера. */
async function openDropdown(triggerText: string): Promise<void> {
	await fireEvent.click(screen.getByText(triggerText));
}

async function pickOption(match: RegExp): Promise<void> {
	const option = screen
		.getAllByRole('option')
		.find((el) => match.test((el.textContent ?? '').trim()));
	if (!option) throw new Error(`option ${match} not found`);
	await fireEvent.click(option);
}

describe('DNSRuleEditModal', () => {
	it('action=evaluate показывает поле тега и скрывает race', async () => {
		render(DNSRuleEditModal, {
			props: {
				...baseProps,
				rule: { domain: ['a.com'], action: 'route', server: 'dns-direct', match_response: 'rd' },
				rules: [evalRD, { domain: ['a.com'], action: 'route', server: 'dns-direct', match_response: 'rd' }],
				ruleIndex: 1,
				onSave: vi.fn(),
			},
		});

		expect(screen.getByLabelText('Race')).toBeTruthy();
		expect(screen.queryByText('Тег ответа')).toBeNull();

		await fireEvent.click(screen.getByText('Evaluate'));

		expect(screen.getByText('Тег ответа')).toBeTruthy();
		expect(screen.queryByLabelText('Race')).toBeNull();
	});

	it('action=respond скрывает выбор сервера', async () => {
		render(DNSRuleEditModal, { props: { ...baseProps, onSave: vi.fn() } });

		expect(screen.getByText('DNS сервер')).toBeTruthy();

		await fireEvent.click(screen.getByText('Respond'));

		expect(screen.queryByText('DNS сервер')).toBeNull();
	});

	it('match_response выкл → секция ответа скрыта; включение показывает rcode/ip_cidr', async () => {
		render(DNSRuleEditModal, {
			props: { ...baseProps, rules: [evalAnon], onSave: vi.fn() },
		});

		expect(screen.getByText('По DNS-ответу')).toBeTruthy();
		expect(screen.queryByText('Rcode')).toBeNull();

		await openDropdown('выкл');
		await pickOption(/анонимный evaluate/);

		expect(screen.getByText('Rcode')).toBeTruthy();
		expect(screen.getByText('IP ответа (CIDR, по строке)')).toBeTruthy();
	});

	it('дропдаун match_response содержит только теги evaluate-правил ВЫШЕ ruleIndex', async () => {
		render(DNSRuleEditModal, {
			props: {
				...baseProps,
				rule: { domain: ['a.com'] },
				rules: [
					{ action: 'evaluate', server: 'dns-direct', tag: 'above' },
					{ domain: ['a.com'] },
					{ action: 'evaluate', server: 'dns-direct', tag: 'below' },
				],
				ruleIndex: 1,
				onSave: vi.fn(),
			},
		});

		await openDropdown('выкл');

		expect(screen.getAllByRole('option').map((el) => el.textContent?.trim())).toEqual([
			'выкл',
			'above',
		]);
	});

	it('нет evaluate-правил выше → подсказка под дропдауном ответа', () => {
		render(DNSRuleEditModal, {
			props: { ...baseProps, rules: [{ domain: ['a.com'], server: 'dns-direct' }], onSave: vi.fn() },
		});

		expect(screen.getByText(/Нет evaluate-правил выше/)).toBeTruthy();
	});

	it('не предлагает geoip и IP rule-set для DNS-правила', async () => {
		const availableRuleSets: SingboxRouterRuleSet[] = [
			{ tag: 'geosite-google', type: 'remote', url: 'https://example.com/geosite-google.srs' },
			{ tag: 'geoip-ru', type: 'remote', url: 'https://example.com/geoip-ru.srs' },
			{ tag: 'custom-ips', type: 'inline', rules: [{ ip_cidr: ['10.0.0.0/8'] }] },
		];
		render(DNSRuleEditModal, { props: { ...baseProps, availableRuleSets, onSave: vi.fn() } });

		await fireEvent.click(screen.getByRole('button', { name: '+' }));

		expect(screen.getAllByRole('option').map((el) => el.textContent?.trim())).toEqual(['geosite-google']);
	});

	it('есть evaluate-правило выше → подсказки нет', () => {
		render(DNSRuleEditModal, {
			props: { ...baseProps, rules: [evalRD], onSave: vi.fn() },
		});

		expect(screen.queryByText(/Нет evaluate-правил выше/)).toBeNull();
	});

	it('respond с одним match_response сохраняется', async () => {
		const onSave = vi.fn().mockResolvedValue(undefined);
		render(DNSRuleEditModal, {
			props: { ...baseProps, rules: [evalRD], onSave },
		});

		await fireEvent.click(screen.getByText('Respond'));
		await openDropdown('выкл');
		await pickOption(/^rd$/);
		await openDropdown('— не задан —');
		await pickOption(/^NOERROR$/);
		await fireEvent.click(screen.getByRole('button', { name: 'Сохранить' }));

		expect(onSave).toHaveBeenCalledWith({
			action: 'respond',
			match_response: 'rd',
			response_rcode: 'NOERROR',
		});
	});

	it('route-правило с match_response открывается в режиме matchers и не теряет поля при save', async () => {
		const onSave = vi.fn().mockResolvedValue(undefined);
		const rule: SingboxRouterDNSRule = {
			action: 'route',
			server: 'dns-direct',
			match_response: 'rd',
			ip_cidr: ['10.0.0.0/8'],
			race: true,
		};
		render(DNSRuleEditModal, {
			props: { ...baseProps, rule, rules: [evalRD, rule], ruleIndex: 1, onSave },
		});

		expect(screen.getByText('Matchers (минимум один)')).toBeTruthy();

		await fireEvent.click(screen.getByRole('button', { name: 'Сохранить' }));

		expect(onSave).toHaveBeenCalledWith({
			action: 'route',
			server: 'dns-direct',
			match_response: 'rd',
			ip_cidr: ['10.0.0.0/8'],
			race: true,
		});
	});

	it('catch-all + evaluate сохраняет action/server/tag', async () => {
		const onSave = vi.fn().mockResolvedValue(undefined);
		render(DNSRuleEditModal, { props: { ...baseProps, onSave } });

		await fireEvent.click(screen.getByText('Catch-all (всё остальное)'));
		await fireEvent.click(screen.getByText('Evaluate'));
		await openDropdown('— выберите —');
		await pickOption(/dns-direct/);
		await fireEvent.input(screen.getByPlaceholderText('rd'), { target: { value: 'rd' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Сохранить' }));

		expect(onSave).toHaveBeenCalledWith({
			action: 'evaluate',
			server: 'dns-direct',
			tag: 'rd',
		});
	});
});
