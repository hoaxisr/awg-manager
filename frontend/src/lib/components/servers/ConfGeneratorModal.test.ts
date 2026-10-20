import { describe, it, expect } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import ConfGeneratorModal from './ConfGeneratorModal.svelte';
import type { ASCParams } from '$lib/types';

const serverConfig = { publicKey: 'SRV=', listenPort: 51820, mtu: 1376, address: '10.0.0.1', peers: [] };
const peer = { publicKey: 'PUB=', description: 'phone', presharedKey: '', allowedIPs: [], address: '10.0.0.2' };

const asc = (o: Record<string, unknown>) => o as unknown as ASCParams;

async function preview(ascParams: ASCParams): Promise<string> {
	render(ConfGeneratorModal, {
		open: true, serverConfig, peer, ascParams, wanIP: '203.0.113.1', onclose: () => {},
	});
	const input = document.querySelector('#private-key') as HTMLInputElement;
	await fireEvent.input(input, { target: { value: 'PRIV=' } });
	return document.querySelector('.conf-preview')?.textContent ?? '';
}

describe('ConfGeneratorModal', () => {
	// F685: системный сервер с ASC 3.x — без HP и хвостов клиент не подключится;
	// Jc = 0 при HP — тоже обфускация, а не «обычный WireGuard».
	it('сервер 3.1 с Jc = 0 получает S/H и параметры 3.x', async () => {
		const conf = await preview(asc({
			jc: 0, jmin: 0, jmax: 0, s1: 16, s2: 17, s3: 18, s4: 19, h1: '1', h2: '2', h3: '3', h4: '4',
			'header-protection-key': 'K=', 'random-trailers': 1,
		}));
		for (const want of ['S1 = 16', 'S4 = 19', 'H1 = 1', 'HeaderProtectionKey = K=', 'RandomTrailers = on']) {
			expect(conf).toContain(want);
		}
	});

	it('сервер без ASC — без параметров обфускации', async () => {
		const conf = await preview(asc({ jc: 0, jmin: 0, jmax: 0, s1: 0, s2: 0, h1: '', h2: '', h3: '', h4: '' }));
		expect(conf).toContain('PrivateKey = PRIV=');
		expect(conf).not.toContain('Jc =');
		expect(conf).not.toContain('HeaderProtectionKey');
	});

	// Паритет с бэкендом (WriteASCConf): пустой H — номер типа (F167), S3/S4 —
	// только если заданы.
	it('пустые H — 1..4, нулевые S3/S4 не пишутся', async () => {
		const conf = await preview(asc({
			jc: 0, jmin: 0, jmax: 0, s1: 16, s2: 17, s3: 0, s4: 0, h1: '', h2: '', h3: '', h4: '',
			'header-protection-key': 'K=', 'random-trailers': 1,
		}));
		for (const want of ['H1 = 1\n', 'H2 = 2\n', 'H3 = 3\n', 'H4 = 4\n']) expect(conf).toContain(want);
		expect(conf).not.toContain('S3 =');
	});
});
