import { describe, it, expect, afterEach } from 'vitest';
import {
	isRoutingSubTabVisible,
	isSectionVisible,
	pathToSection,
	sectionLabel,
	usageLevelLabel,
} from './usageLevel';
import { locale } from '$lib/i18n';

describe('секция proxy', () => {
	// Одна секция на обе вкладки страницы «Прокси», порог «Расширенный»:
	// раздельного гейта «Выход» / «Раздача» нет (ia.md §1.1).
	it('скрыта на базовом уровне и видна с расширенного', () => {
		expect(isSectionVisible('basic', 'proxy')).toBe(false);
		expect(isSectionVisible('advanced', 'proxy')).toBe(true);
		expect(isSectionVisible('expert', 'proxy')).toBe(true);
	});

	// Старые адреса — редиректы на /proxy: гейт обязан пускать их к тому же порогу,
	// иначе редирект упрётся в защиту маршрута раньше, чем сработает.
	it('покрывает и легаси-адреса /freeturn и /wdtt', () => {
		expect(pathToSection('/proxy')).toBe('proxy');
		expect(pathToSection('/freeturn/captcha')).toBe('proxy');
		expect(pathToSection('/wdtt')).toBe('proxy');
	});
});

describe('isRoutingSubTabVisible', () => {
	// Политики доступа — базовая функция роутера, а не экспертная настройка:
	// вкладка видна на всех уровнях.
	it('shows accessPolicies on the basic level', () => {
		expect(isRoutingSubTabVisible('basic', 'accessPolicies')).toBe(true);
	});
});

describe('подписи уровней и разделов (i18n)', () => {
	afterEach(() => {
		locale.set('ru');
		localStorage.clear();
	});

	it('usageLevelLabel и sectionLabel следуют за языком интерфейса', () => {
		expect(usageLevelLabel('advanced')).toBe('Расширенный');
		expect(sectionLabel('diagnostics')).toBe('Инструменты');
		locale.set('en');
		expect(usageLevelLabel('advanced')).toBe('Advanced');
		expect(sectionLabel('diagnostics')).toBe('Tools');
		expect(sectionLabel('singboxTunnels')).toBe('Sing-box');
	});
});
