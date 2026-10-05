import { describe, it, expect } from 'vitest';
import { render } from '@testing-library/svelte';
import RichText from './RichText.svelte';

describe('RichText', () => {
	it('выводит разметку из словаря элементами, с классом и ссылкой', () => {
		const { container } = render(RichText, {
			props: {
				text: 'Подписка <strong>Дом</strong> и <code class="mono">Proxy1</code>, см. <a>сайт</a>.',
				href: 'https://example.org',
			},
		});
		expect(container.querySelector('strong')?.textContent).toBe('Дом');
		expect(container.querySelector('code.mono')?.textContent).toBe('Proxy1');
		expect(container.querySelector('a')?.getAttribute('href')).toBe('https://example.org');
		expect(container.textContent).toBe('Подписка Дом и Proxy1, см. сайт.');
	});

	it('подставленное значение не становится HTML', () => {
		const { container } = render(RichText, {
			props: { text: 'Группа <strong><img src=x onerror=alert(1)></strong> удалена' },
		});
		expect(container.querySelector('img')).toBeNull();
		expect(container.querySelector('strong')?.textContent).toBe('<img src=x onerror=alert(1)>');
	});
});
