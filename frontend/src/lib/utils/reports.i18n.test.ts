import { describe, it, expect, afterEach } from 'vitest';
import { render } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import { locale, m } from '$lib/i18n';
import { routerClientRows, awgmServicesRows, buildAwgmServicesSnapshot, formatAboutReport } from './about-device';
import { buildSuggestionIssueUrl } from './githubFeedback';
import TermsPage from '../../routes/terms/+page.svelte';

afterEach(() => {
	locale.set('ru');
	localStorage.clear();
});

const snapshot = () =>
	buildAwgmServicesSnapshot({
		level: 'advanced',
		theme: null,
		settings: null,
		authDisabled: true,
		authenticated: false,
		login: null,
		singbox: null,
		hydra: null,
		deviceProxy: null,
		deviceProxyRuntime: null,
		clientRoutesTotal: 2,
		clientRoutesEnabled: 1,
		clientRoutesLoaded: true,
		dnsRoutesTotal: 0,
		dnsRoutesEnabled: 0,
		awgRunning: 0,
		awgTotal: 0,
		subscriptionsEnabled: 0,
		subscriptionsTotal: 0,
	});

describe('reports i18n', () => {
	it('about-device: подписи и значения следуют за языком, id стабилен', () => {
		const ru = awgmServicesRows(snapshot()).find((r) => r.id === 'clientRoutes');
		expect(ru?.label).toBe('VPN для устройств');
		expect(ru?.value).toBe('1 вкл / 2 всего');
		expect(routerClientRows(null)[0]).toMatchObject({ id: 'status', label: 'Статус', value: 'Не загружено' });
		expect(formatAboutReport([]).split('\n')[0]).toBe('AWG Manager — окружение');

		locale.set('en');
		const en = awgmServicesRows(snapshot()).find((r) => r.id === 'clientRoutes');
		expect(en?.label).toBe('VPN for devices');
		expect(en?.value).toBe('1 on / 2 total');
		expect(routerClientRows(null)[0]).toMatchObject({ id: 'status', label: 'Status', value: 'Not loaded' });
		expect(formatAboutReport([]).split('\n')[0]).toBe('AWG Manager — environment');
	});

	it('шаблоны issue: заголовки Markdown на языке интерфейса', () => {
		expect(m.diag_issue_heading_what()).toBe('## Что произошло');
		expect(m.diag_issue_context_page({ path: '/x' })).toBe('- Страница: `/x`');
		const ruBody = new URL(buildSuggestionIssueUrl()).searchParams.get('body');
		expect(ruBody).toContain('## Что хотите сообщить');

		locale.set('en');
		expect(m.diag_issue_heading_what()).toBe('## What happened');
		expect(m.diag_issue_context_page({ path: '/x' })).toBe('- Page: `/x`');
		const enUrl = new URL(buildSuggestionIssueUrl());
		expect(enUrl.searchParams.get('body')).toContain('## What would you like to report');
		expect(enUrl.searchParams.get('title')).toBe('AWG Manager message or suggestion');
	});

	it('terms: страница рендерится на языке интерфейса', () => {
		const { container } = render(TermsPage);
		expect(container.querySelector('h1')?.textContent).toBe('Пользовательское соглашение');
		expect(container.querySelector('.intro')?.textContent).toContain('AWG Manager — независимый open-source инструмент');
		expect(container.querySelectorAll('.intro strong')).toHaveLength(1);
		expect(container.querySelector('.faq-a a')?.getAttribute('href')).toBe('https://aviasales.ru');

		locale.set('en');
		flushSync();
		expect(container.querySelector('h1')?.textContent).toBe('User Agreement');
		expect(container.querySelector('.intro')?.textContent).toContain('is an independent open-source tool');
		expect(container.querySelector('.faq-a a')?.textContent).toBe('aviasales.ru');
		expect(container.textContent).not.toMatch(/[А-Яа-яЁё]/);
	});
});
