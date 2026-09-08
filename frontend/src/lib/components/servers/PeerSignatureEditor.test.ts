import { describe, it, expect, vi } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import PeerSignatureEditor from './PeerSignatureEditor.svelte';

const generateSignature = vi.fn(async (protocol: string) => ({
	ok: true,
	source: 'generated',
	protocol,
	byteSize: 3,
	packets: { i1: '<b 0x0102>', i2: '', i3: '', i4: '', i5: '' },
}));

vi.mock('$lib/api/client', () => ({ api: { generateSignature: (p: string) => generateSignature(p) } }));
vi.mock('$lib/stores/notifications', () => ({ notifications: { success: vi.fn(), error: vi.fn() } }));

describe('PeerSignatureEditor', () => {
	it('generate replaces all five fields and reports profile', async () => {
		const onchange = vi.fn();
		const { getByText } = render(PeerSignatureEditor, {
			profile: 'dns',
			packets: { i1: 'old', i2: 'old2', i3: '', i4: '', i5: '' },
			onchange,
		});

		await fireEvent.click(getByText('Сгенерировать'));

		await waitFor(() => expect(onchange).toHaveBeenCalled());
		expect(generateSignature).toHaveBeenCalledWith('dns');
		expect(onchange).toHaveBeenLastCalledWith({
			profile: 'dns',
			packets: { i1: '<b 0x0102>', i2: '', i3: '', i4: '', i5: '' },
		});
	});

	it('generates with quic_initial when no profile is set', async () => {
		const onchange = vi.fn();
		const { getByText } = render(PeerSignatureEditor, {
			profile: '',
			packets: { i1: '', i2: '', i3: '', i4: '', i5: '' },
			onchange,
		});

		await fireEvent.click(getByText('Сгенерировать'));

		await waitFor(() => expect(onchange).toHaveBeenCalled());
		expect(generateSignature).toHaveBeenLastCalledWith('quic_initial');
		expect(onchange).toHaveBeenLastCalledWith({
			profile: 'quic_initial',
			packets: { i1: '<b 0x0102>', i2: '', i3: '', i4: '', i5: '' },
		});
	});

	it('shows byte size against the 4096 limit', () => {
		const { getByText } = render(PeerSignatureEditor, {
			profile: '',
			packets: { i1: '<r 10>', i2: '', i3: '', i4: '', i5: '' },
			onchange: vi.fn(),
		});
		expect(getByText(/10 \/ 4096 байт/)).toBeTruthy();
	});

	it('reports a hand-edited packet field', async () => {
		const onchange = vi.fn();
		const { getByLabelText } = render(PeerSignatureEditor, {
			profile: 'dns',
			packets: { i1: 'a', i2: '', i3: '', i4: '', i5: '' },
			onchange,
		});

		await fireEvent.input(getByLabelText('I2'), { target: { value: '<r 4>' } });

		expect(onchange).toHaveBeenLastCalledWith({
			profile: 'dns',
			packets: { i1: 'a', i2: '<r 4>', i3: '', i4: '', i5: '' },
		});
	});
});
