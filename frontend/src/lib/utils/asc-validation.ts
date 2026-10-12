import { m } from '$lib/i18n';
import type { ASCParams, ASCParamsExtended } from '$lib/types';

const REQUIRED_NUMERIC = ['jc', 'jmin', 'jmax', 's1', 's2'] as const;
const REQUIRED_TEXT = ['h1', 'h2', 'h3', 'h4'] as const;

function isZeroLike(v: unknown): boolean {
	return v === 0 || v === '0';
}

function isMissing(v: unknown): boolean {
	return v === undefined || v === null || v === '';
}

export function isExtendedASCParams(params: ASCParams): params is ASCParamsExtended {
	return 's3' in params;
}

export function isZeroASCState(params: ASCParams): boolean {
	for (const key of REQUIRED_NUMERIC) {
		if (!isZeroLike(params[key])) {
			return false;
		}
	}

	for (const key of REQUIRED_TEXT) {
		if (String(params[key] ?? '').trim() !== '') {
			return false;
		}
	}

	if ('s3' in params) {
		const raw = (params).s3 as unknown;
		if (!(raw === undefined || raw === null || raw === '' || isZeroLike(raw))) {
			return false;
		}
	}

	if ('s4' in params) {
		const raw = (params).s4 as unknown;
		if (!(raw === undefined || raw === null || raw === '' || isZeroLike(raw))) {
			return false;
		}
	}

	return true;
}

export function validateASCBeforeSave(params: ASCParams): string[] {
	if (isZeroASCState(params)) {
		return [];
	}

	const errors: string[] = [];
	const invalidNumeric: string[] = [];
	const emptyText: string[] = [];

	for (const key of REQUIRED_NUMERIC) {
		const raw = params[key] as unknown;
		const value = Number(raw);
		if (isMissing(raw) || !Number.isFinite(value) || value <= 0) {
			invalidNumeric.push(key.toUpperCase());
		}
	}

	for (const key of REQUIRED_TEXT) {
		const value = String(params[key] ?? '').trim();
		if (!value) {
			emptyText.push(key.toUpperCase());
		}
	}

	if ('s3' in params || 's4' in params) {
		const ext = params as ASCParamsExtended;
		const s3 = Number(ext.s3);
		const s4 = Number(ext.s4);
		if (isMissing(ext.s3) || !Number.isFinite(s3) || s3 <= 0) {
			invalidNumeric.push('S3');
		}
		if (isMissing(ext.s4) || !Number.isFinite(s4) || s4 <= 0) {
			invalidNumeric.push('S4');
		}
	}

	if (emptyText.length > 0) {
		errors.push(m.asc_validation_fill_params({ list: emptyText.join(', ') }));
	}

	if (invalidNumeric.length > 0) {
		errors.push(m.asc_validation_params_positive({ list: invalidNumeric.join(', ') }));
	}

	const jmin = Number(params.jmin);
	const jmax = Number(params.jmax);
	if (Number.isFinite(jmin) && Number.isFinite(jmax) && jmin > 0 && jmax > 0 && jmax <= jmin) {
		errors.push(m.asc_validation_jmax_gt_jmin());
	}

	return errors;
}

export function applyDisabledASCState(params: ASCParams): void {
	params.jc = 0;
	params.jmin = 0;
	params.jmax = 0;
	params.s1 = 0;
	params.s2 = 0;
	params.h1 = '';
	params.h2 = '';
	params.h3 = '';
	params.h4 = '';

	if ('s3' in params || 's4' in params) {
		const ext = params as ASCParamsExtended;
		ext.s3 = 0;
		ext.s4 = 0;
		ext.i1 = '';
		ext.i2 = '';
		ext.i3 = '';
		ext.i4 = '';
		ext.i5 = '';
	}
}
