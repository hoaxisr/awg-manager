import { describe, expect, it, vi } from 'vitest';
import { createRawSnippet } from 'svelte';
import { render, screen } from '@testing-library/svelte';
import { m } from '$lib/i18n';
import type { KeenDNSDiscovery } from '$lib/types/serverWizard';
import { validateKeenDNSLabelClient } from '../externalAccessModel';
import KeenDnsAddressBox from '../KeenDnsAddressBox.svelte';

function mount(label: string, labelConflict = '') {
	render(KeenDnsAddressBox, {
		props: {
			variant: 'direct', mode: 'create', label, labelConflict,
			keendnsInfo: {
				state: 'supported', enabled: true, domain: 'home.example.keenetic.pro', mode: 'direct',
				cert_ok: true, publications: [], wan_ip_private: false,
			} satisfies KeenDNSDiscovery,
			keendnsAvailable: true, validation: validateKeenDNSLabelClient(label),
			previewFqdn: `${label}.home.example.keenetic.pro`, publications: [],
			onsetmode: vi.fn(), onapplypublication: vi.fn(), onsync: vi.fn(),
			manual: createRawSnippet(() => ({ render: () => '<i></i>' })),
		},
	});
	return screen.queryByText(m.servers_ext_wiz_kd_preview_direct(), { exact: false });
}

// Мутации: показывать «адрес будет создан» при конфликте или негодной метке.
describe('KeenDnsAddressBox: «адрес будет создан»', () => {
	it('при конфликте метки строки нет, ошибка есть', () => {
		expect(mount('connect', 'Метка занята')).toBeNull();
		expect(screen.getByText('Метка занята')).not.toBeNull();
	});
	it('при негодной метке строки нет', () => {
		expect(mount('-bad-')).toBeNull();
	});
	it('при годной свободной метке строка есть', () => {
		expect(mount('connect')).not.toBeNull();
	});
});
