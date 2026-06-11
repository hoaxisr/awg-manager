import type { OutboundDisplay, OutboundKind } from './types';

/** Визуальная категория бейджа outbound в RuleCard. */
export type OutboundTileTone =
	| 'proxy'
	| 'awg'
	| 'subscription'
	| 'composite'
	| 'block'
	| 'direct'
	| 'via-route'
	| 'invalid'
	| 'system'
	| 'unknown';

export function toneClass(tone: OutboundTileTone): string {
	return `tone-${tone}`;
}

export function toneForKind(kind: OutboundKind): OutboundTileTone {
	switch (kind) {
		case 'proxy':
		case 'tunnel':
			return 'proxy';
		case 'awg':
			return 'awg';
		case 'subscription':
			return 'subscription';
		case 'composite':
			return 'composite';
		case 'block':
			return 'block';
		case 'direct':
			return 'direct';
		case 'via-route':
			return 'via-route';
		case 'sniff':
		case 'hijack-dns':
			return 'system';
		default:
			return 'unknown';
	}
}

export function displayTone(outbound: OutboundDisplay): OutboundTileTone {
	return outbound.tone ?? toneForKind(outbound.kind);
}

export function isCompositeDisplay(outbound: OutboundDisplay): boolean {
	return (outbound.kind === 'composite' || outbound.kind === 'subscription')
		&& !!outbound.activeMemberLabel;
}
