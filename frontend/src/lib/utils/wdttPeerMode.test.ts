import { describe, expect, it } from 'vitest';
import type { WdttClientConfig } from '$lib/types';
import {
	activePeerForMode,
	hydratePeerSlots,
	switchConnMode,
	syncActivePeer
} from './wdttPeerMode';

function cfg(partial: Partial<WdttClientConfig> = {}): WdttClientConfig {
	return {
		listen: '127.0.0.1:9000',
		peer: '',
		password: '',
		vkHashes: '',
		workers: 24,
		obfs: 'audio',
		fingerprint: 'chrome',
		captchaMode: 'rjs',
		...partial
	};
}

describe('wdttPeerMode', () => {
	it('switchConnMode preserves separate WG and Raw peers', () => {
		const c = cfg({ peer: '1.1.1.1:56002', connMode: 'wg' });
		hydratePeerSlots(c);
		switchConnMode(c, 'raw');
		c.peerRaw = '1.1.1.1:56003';
		switchConnMode(c, 'wg');
		expect(c.peer).toBe('1.1.1.1:56002');
		switchConnMode(c, 'raw');
		expect(c.peer).toBe('1.1.1.1:56003');
	});

	it('syncActivePeer writes active mode peer', () => {
		const c = cfg({
			connMode: 'raw',
			peer: 'legacy:56002',
			peerRaw: '1.1.1.1:56003',
			peerWg: '1.1.1.1:56002'
		});
		syncActivePeer(c);
		expect(c.peer).toBe('1.1.1.1:56003');
		expect(activePeerForMode(c)).toBe('1.1.1.1:56003');
	});
});
