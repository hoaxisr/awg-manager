import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/svelte';
import RuleEditModal from './RuleEditModal.svelte';
import type { SingboxRouterRule } from '$lib/types';

class ResizeObserverStub {
	observe(): void {}
	disconnect(): void {}
}
vi.stubGlobal('ResizeObserver', ResizeObserverStub);

const baseProps = {
	outboundOptions: [{ group: 'Туннели', items: [{ value: 'vpn', label: 'vpn' }] }],
	availableRuleSets: [],
	onClose: vi.fn(),
};

describe('RuleEditModal', () => {
	it('переносит матчеры, которых нет в форме, вместо их потери', async () => {
		const onSave = vi.fn();
		const rule: SingboxRouterRule = {
			domain: ['exact.host'],
			domain_suffix: ['example.com'],
			protocol: 'tls',
			ip_is_private: true,
			inbound: ['tproxy-in'],
			action: 'route',
			outbound: 'vpn',
		};
		render(RuleEditModal, { props: { ...baseProps, rule, onSave } });

		await fireEvent.click(screen.getByText('Сохранить'));

		expect(onSave).toHaveBeenCalledTimes(1);
		const saved = onSave.mock.calls[0][0] as SingboxRouterRule;
		expect(saved.domain).toEqual(['exact.host']);
		expect(saved.protocol).toBe('tls');
		expect(saved.ip_is_private).toBe(true);
		expect(saved.inbound).toEqual(['tproxy-in']);
		expect(saved.domain_suffix).toEqual(['example.com']);
	});

	it('называет условия, которых нет в форме', () => {
		const rule: SingboxRouterRule = {
			domain: ['exact.host'],
			domain_suffix: ['example.com'],
			protocol: 'tls',
			ip_is_private: true,
			inbound: ['tproxy-in'],
			action: 'route',
			outbound: 'vpn',
		};
		render(RuleEditModal, { props: { ...baseProps, rule, onSave: vi.fn() } });

		expect(screen.getByText(/условия, которых нет в этой форме/i)).toBeTruthy();
		expect(screen.getByText('точные домены: exact.host')).toBeTruthy();
		expect(screen.getByText('прикладной протокол: tls')).toBeTruthy();
		expect(screen.getByText('только локальные адреса назначения')).toBeTruthy();
		expect(screen.getByText('вход: tproxy-in')).toBeTruthy();
	});

	it('предупреждает, что сохранение заменит нераспознанную вложенную структуру', () => {
		const rule: SingboxRouterRule = {
			type: 'logical',
			mode: 'or',
			rules: [{ domain_suffix: ['a.com'] }, { domain_suffix: ['b.com'] }, { port: [443] }],
			action: 'route',
			outbound: 'vpn',
		};
		render(RuleEditModal, { props: { ...baseProps, rule, onSave: vi.fn() } });
		expect(screen.getByText(/вложенной логической структурой/i)).toBeTruthy();
	});

	it('у нашей логической формы такого предупреждения нет', () => {
		const rule: SingboxRouterRule = {
			type: 'logical',
			mode: 'or',
			rules: [{ rule_set: ['geosite-discord'] }, { ip_cidr: ['66.22.192.0/18'] }],
			action: 'route',
			outbound: 'vpn',
		};
		render(RuleEditModal, {
			props: {
				...baseProps,
				rule,
				availableRuleSets: [{ tag: 'geosite-discord', type: 'remote', url: 'https://x/y.srs' }],
				onSave: vi.fn(),
			},
		});
		expect(screen.queryByText(/вложенной логической структурой/i)).toBeNull();
	});

	it('у обычного правила предупреждения нет', () => {
		render(RuleEditModal, {
			props: {
				...baseProps,
				rule: { domain_suffix: ['example.com'], action: 'route', outbound: 'vpn' },
				onSave: vi.fn(),
			},
		});
		expect(screen.queryByText(/условия, которых нет в этой форме/i)).toBeNull();
	});

	it('логическую форму «набор ИЛИ свои адреса» показывает и сохраняет плоской', async () => {
		const onSave = vi.fn();
		const rule: SingboxRouterRule = {
			type: 'logical',
			mode: 'or',
			rules: [{ rule_set: ['geosite-discord'] }, { ip_cidr: ['66.22.192.0/18'] }],
			action: 'route',
			outbound: 'vpn',
		};
		render(RuleEditModal, {
			props: {
				...baseProps,
				rule,
				availableRuleSets: [{ tag: 'geosite-discord', type: 'remote', url: 'https://x/y.srs' }],
				onSave,
			},
		});

		// Поля открылись содержимым веток, а не пустыми.
		expect(screen.getByDisplayValue('66.22.192.0/18')).toBeTruthy();

		await fireEvent.click(screen.getByText('Сохранить'));

		const saved = onSave.mock.calls[0][0] as SingboxRouterRule;
		expect(saved.ip_cidr).toEqual(['66.22.192.0/18']);
		expect(saved.rule_set).toEqual(['geosite-discord']);
		expect(saved.type).toBeUndefined();
	});

	it('вводит MAC устройства и сохраняет его в нижнем регистре', async () => {
		const onSave = vi.fn();
		const rule: SingboxRouterRule = {
			domain_suffix: ['example.com'],
			action: 'route',
			outbound: 'vpn',
		};
		render(RuleEditModal, { props: { ...baseProps, rule, onSave } });

		const macField = screen.getByPlaceholderText('aa:bb:cc:dd:ee:ff');
		await fireEvent.input(macField, { target: { value: 'AA:BB:CC:DD:EE:FF' } });

		await fireEvent.click(screen.getByText('Сохранить'));

		const saved = onSave.mock.calls[0][0] as SingboxRouterRule;
		expect(saved.source_mac_address).toEqual(['aa:bb:cc:dd:ee:ff']);
	});
});
