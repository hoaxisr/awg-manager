import { describe, it, expect } from 'vitest';
import { CAPTURE_MODES, captureModeCopy, captureModeOptions } from './captureModes';
import { humanLabel } from '$lib/components/fakeip/switchConsequences';
import { deriveEnginePill } from './engineRunState';

describe('captureModes', () => {
	// Пикер выбирает РЕЖИМ ЗАХВАТА, а 'off' — это выключение движка (кнопка в
	// шапке). Четвёртая карточка в пикере означала бы два способа выключить.
	it('в пикере ровно три режима и «off» среди них нет', () => {
		expect(CAPTURE_MODES).toEqual(['tproxy', 'policy-tun', 'fakeip-tun']);
		expect(CAPTURE_MODES).not.toContain('off');
	});

	// Название режима уже живёт в бейдже сайдбара, пилюле шапки и экране
	// подтверждения — все они читают humanLabel. Своя копия названия здесь
	// означала бы, что один и тот же режим называется по-разному на одном экране.
	it.each(CAPTURE_MODES)('название режима %s взято из общего humanLabel', (mode) => {
		expect(captureModeCopy(mode).label).toBe(humanLabel(mode));
	});

	it('у каждого режима своё объяснение и своя техническая подпись', () => {
		const opts = captureModeOptions();
		expect(opts).toHaveLength(3);
		expect(new Set(opts.map((o) => o.summary)).size).toBe(3);
		expect(new Set(opts.map((o) => o.tech)).size).toBe(3);
		expect(opts.every((o) => o.summary.trim() !== '' && o.tech.trim() !== '')).toBe(true);
	});

	// Подпись говорит, ЧЕМ режим поднимается; подсказка сбоя — что из этого не
	// поднялось. Разойдясь, они стали бы двумя описаниями одного режима.
	it.each([
		['tproxy', /PREROUTING/],
		['policy-tun', /NDMS/],
		['fakeip-tun', /fakeip-пул/],
	] as const)('техническая подпись %s совпадает по составу с подсказкой сбоя', (mode, re) => {
		expect(captureModeCopy(mode).tech).toMatch(re);
		expect(deriveEnginePill({ enabled: true, active: false, routingMode: mode }).hint).toMatch(re);
	});

	it('порядок вариантов пикера — порядок CAPTURE_MODES', () => {
		expect(captureModeOptions().map((o) => o.mode)).toEqual([...CAPTURE_MODES]);
	});
});
