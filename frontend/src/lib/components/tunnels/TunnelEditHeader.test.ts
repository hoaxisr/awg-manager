import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import TunnelEditHeader from './TunnelEditHeader.svelte';

// Страница туннеля: state=disabled при включённом туннеле (откат
// неудавшегося старта) — не «Отключён». Мутация «enabled не передан /
// не учтён» → красный.
describe('TunnelEditHeader: включён, но не запустился', () => {
	const props = { tunnelName: 'NL', saving: false, actionStatus: null, onSaveAndStart: () => {} };

	it('disabled + enabled — «Не запустился — будет повтор»', () => {
		render(TunnelEditHeader, { props: { ...props, tunnelState: 'disabled', enabled: true } });
		expect(screen.getByText('Не запустился — будет повтор')).toBeTruthy();
		expect(screen.queryByText('Отключён')).toBeNull();
	});

	it('disabled + !enabled — «Отключён»', () => {
		render(TunnelEditHeader, { props: { ...props, tunnelState: 'disabled', enabled: false } });
		expect(screen.getByText('Отключён')).toBeTruthy();
	});
});
