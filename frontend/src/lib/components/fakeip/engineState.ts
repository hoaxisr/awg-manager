// FE-spec §12.1 — coarse engine-state derivation for the FakeIP page.
//
// The page renders one of four screens depending on the FakeIP engine state.
// The inputs come from three independent sources (verified against the real
// backend — do NOT collapse them):
//   - routingMode: from SingboxRouterSettings (settings, NOT status). Absent on
//     legacy payloads → treated as 'tproxy' (the legacy default), i.e. not
//     fakeip-tun.
//   - running:     SingboxStatus.running (the singbox daemon is up).
//   - clashReachable: whether the Clash runtime API answers (live blocks).
//
// This is a pure function so the full state matrix can be unit-tested without
// stores or components.

export type FakeIPEngineState = 'not-fakeip' | 'stopped' | 'clash-down' | 'live';

export function deriveFakeIPEngineState(input: {
	routingMode: 'tproxy' | 'fakeip-tun' | undefined;
	running: boolean;
	clashReachable: boolean;
}): FakeIPEngineState {
	if (input.routingMode !== 'fakeip-tun') return 'not-fakeip';
	if (!input.running) return 'stopped';
	if (!input.clashReachable) return 'clash-down';
	return 'live';
}
