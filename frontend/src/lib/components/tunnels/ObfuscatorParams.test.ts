import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import ObfuscatorParams from './ObfuscatorParams.svelte';
import type { TunnelObfuscator } from '$lib/types';

const base: TunnelObfuscator = { flavor: 'phobos', target: 'h:1', key: 'k', masking: 'STUN', maxDummy: 4, localPort: 39000 };

describe('ObfuscatorParams', () => {
	it('phobos показывает obfuscate-bytes и MEDIA', () => {
		render(ObfuscatorParams, { props: { obfuscator: base } });
		expect(screen.getByLabelText(/obfuscate-bytes/)).toBeTruthy();
		expect(screen.getByRole('option', { name: 'MEDIA' })).toBeTruthy();
		expect(screen.getByText('Phobos')).toBeTruthy();
	});
	it('clusterm прячет obfuscate-bytes и MEDIA', () => {
		render(ObfuscatorParams, { props: { obfuscator: { ...base, flavor: 'clusterm' } } });
		expect(screen.queryByLabelText(/obfuscate-bytes/)).toBeNull();
		expect(screen.queryByRole('option', { name: 'MEDIA' })).toBeNull();
	});
	it('показывает ошибку target', () => {
		render(ObfuscatorParams, { props: { obfuscator: base, error: 'Нужен host:port' } });
		expect(screen.getByText('Нужен host:port')).toBeTruthy();
	});
});
