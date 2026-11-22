import { describe, expect, it } from 'vitest';
import {
	formatUrlsForTextarea,
	hasMailruLoginCookie,
	laneCountLabel,
	normalizeTransportUrls,
	splitTransportUrls,
} from './openfluxUrls';

describe('openfluxUrls', () => {
	it('разбирает ссылки по переносам строк, запятым, пробелам и |', () => {
		const raw =
			'  https://cloud.mail.ru/public/AAA/111 \n\n https://cloud.mail.ru/public/BBB/222 | https://cloud.mail.ru/public/CCC/333 ,  ';
		expect(splitTransportUrls(raw)).toEqual([
			'https://cloud.mail.ru/public/AAA/111',
			'https://cloud.mail.ru/public/BBB/222',
			'https://cloud.mail.ru/public/CCC/333',
		]);
		expect(normalizeTransportUrls(raw)).toBe(
			'https://cloud.mail.ru/public/AAA/111,https://cloud.mail.ru/public/BBB/222,https://cloud.mail.ru/public/CCC/333',
		);
	});

	it('делает обратимое преобразование textarea <-> config для одной и нескольких ссылок', () => {
		expect(formatUrlsForTextarea('')).toBe('');
		expect(formatUrlsForTextarea(undefined)).toBe('');

		const saved = 'https://cloud.mail.ru/public/A/1,https://cloud.mail.ru/public/B/2';
		const forUi = formatUrlsForTextarea(saved);
		expect(forUi).toBe('https://cloud.mail.ru/public/A/1\nhttps://cloud.mail.ru/public/B/2');
		expect(normalizeTransportUrls(forUi)).toBe(saved);
	});

	it('склоняет счётчик линий страйпа', () => {
		expect(laneCountLabel(2)).toBe('2 линии · stripe');
		expect(laneCountLabel(4)).toBe('4 линии · stripe');
		expect(laneCountLabel(5)).toBe('5 линий · stripe');
		expect(laneCountLabel(11)).toBe('11 линий · stripe');
		expect(laneCountLabel(21)).toBe('21 линия · stripe');
	});

	it('распознаёт Mpop в Cookie-заголовке, Netscape и JSON форматах', () => {
		expect(hasMailruLoginCookie('')).toBe(false);
		expect(hasMailruLoginCookie('sdc=abc; t=123')).toBe(false);
		expect(hasMailruLoginCookie('Cookie: sdc=abc; Mpop=1:token:user@mail.ru:1')).toBe(true);
		expect(hasMailruLoginCookie('Mpop=1:token')).toBe(true);
		expect(
			hasMailruLoginCookie('#HttpOnly_.mail.ru\tTRUE\t/\tTRUE\t1893456000\tMpop\tval123'),
		).toBe(true);
		expect(hasMailruLoginCookie('[{"name":"Mpop","value":"val123"}]')).toBe(true);
		expect(hasMailruLoginCookie('{"https://cloud.mail.ru/public/a/b":{"Mpop":"val123"}}')).toBe(true);
	});
});
