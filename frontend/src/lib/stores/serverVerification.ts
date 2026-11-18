import { writable } from 'svelte/store';
import type { VerificationReport, WizardKind } from '$lib/types/serverWizard';

export interface ServerVerificationState {
	xray: VerificationReport | null;
	tgwebproxy: VerificationReport | null;
}

export const lastServerVerification = writable<ServerVerificationState>({
	xray: null,
	tgwebproxy: null,
});

export function recordServerVerification(kind: WizardKind, report: VerificationReport): void {
	lastServerVerification.update((cur) => ({
		xray: kind === 'xray' || kind === 'both' ? report : cur.xray,
		tgwebproxy: kind === 'tgwebproxy' || kind === 'both' ? report : cur.tgwebproxy,
	}));
}

export function pickFresherVerificationReport(
	candidate: VerificationReport | null,
	current: VerificationReport | null,
): VerificationReport | null {
	if (!candidate) return current;
	if (!current) return candidate;
	const candTime = Date.parse(candidate.checked_at || '');
	const curTime = Date.parse(current.checked_at || '');
	if (!Number.isNaN(candTime) && !Number.isNaN(curTime)) {
		return candTime >= curTime ? candidate : current;
	}
	return candidate;
}

export function resetServerVerification(): void {
	lastServerVerification.set({ xray: null, tgwebproxy: null });
}
