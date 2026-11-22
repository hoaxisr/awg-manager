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

export function isEquivalentVerificationReport(
	a: VerificationReport | null,
	b: VerificationReport | null,
): boolean {
	if (a === b) return true;
	if (!a || !b) return false;
	return (
		(a.checked_at || '') === (b.checked_at || '') &&
		(a.state_fingerprint || '') === (b.state_fingerprint || '') &&
		a.overall_status === b.overall_status &&
		(a.checks?.length ?? 0) === (b.checks?.length ?? 0)
	);
}

export function pickFresherVerificationReport(
	candidate: VerificationReport | null,
	current: VerificationReport | null,
): VerificationReport | null {
	if (!candidate) return current;
	if (!current) return candidate;
	if (candidate === current) return current;
	const candTime = Date.parse(candidate.checked_at || '');
	const curTime = Date.parse(current.checked_at || '');
	if (!Number.isNaN(candTime) && !Number.isNaN(curTime)) {
		if (candTime > curTime) return candidate;
		if (candTime < curTime) return current;
		return isEquivalentVerificationReport(candidate, current) ? current : candidate;
	}
	return isEquivalentVerificationReport(candidate, current) ? current : candidate;
}

export function clearServerVerification(kind: WizardKind): void {
	lastServerVerification.update((cur) => ({
		xray: kind === 'xray' || kind === 'both' ? null : cur.xray,
		tgwebproxy: kind === 'tgwebproxy' || kind === 'both' ? null : cur.tgwebproxy,
	}));
}

export function resetServerVerification(): void {
	lastServerVerification.set({ xray: null, tgwebproxy: null });
}
