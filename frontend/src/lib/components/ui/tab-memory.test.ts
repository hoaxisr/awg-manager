import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, waitFor } from '@testing-library/svelte';
import { page } from '$app/stores';
import type { Writable } from 'svelte/store';
import { tabMemoryKey, readTabMemory, writeTabMemory } from './tab-memory';
import Host from './TabsMemoryHost.svelte';

vi.mock('$app/stores', async () => {
    const { writable } = await import('svelte/store');
    return { page: writable({ url: new URL('http://localhost/routing'), route: { id: '/routing' } }) };
});
vi.mock('$app/navigation', () => ({ goto: vi.fn().mockResolvedValue(undefined) }));

const tabs = [{ id: 'first', label: 'First' }, { id: 'second', label: 'Second' }];
const key = tabMemoryKey('routing-sections');
const location = page as unknown as Writable<{ url: URL; route: { id: string } }>;

vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} });

beforeEach(() => {
    localStorage.clear();
    location.set({ url: new URL('http://localhost/routing'), route: { id: '/routing' } });
});

describe('tab memory across sections', () => {
    it('keeps explicitly keyed sections separate', () => {
        writeTabMemory(key, 'second');
        expect(readTabMemory(key, ['first', 'second'])).toBe('second');
        expect(readTabMemory(tabMemoryKey('diagnostics-sections'), ['first', 'second'])).toBeNull();
        expect(readTabMemory(tabMemoryKey('routing-hostlists'), ['first', 'second'])).toBeNull();
    });

    it('ignores unknown tabs and unavailable browser storage', () => {
        writeTabMemory(key, 'removed');
        expect(readTabMemory(key, ['first'])).toBeNull();
        const spy = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('unavailable'); });
        expect(readTabMemory(key, ['first'])).toBeNull();
        spy.mockRestore();
    });

    it('restores after installation-dependent tabs finish loading', async () => {
        writeTabMemory(key, 'second');
        const view = render(Host, { tabs: [tabs[0]], ready: false, persistKey: 'routing-sections' });
        expect(view.getByTestId('active').textContent).toBe('first');
        await view.rerender({ tabs, ready: true });
        await waitFor(() => expect(view.getByTestId('active').textContent).toBe('second'));
    });

    it('does not pull the user to a remembered tab that appears after readiness', async () => {
        writeTabMemory(key, 'second');
        const view = render(Host, { tabs: [tabs[0]], ready: true, persistKey: 'routing-sections' });
        await view.rerender({ tabs, ready: true });
        await Promise.resolve();
        expect(view.getByTestId('active').textContent).toBe('first');
    });

    it('keeps a manual choice when a remembered optional section arrives later', async () => {
        writeTabMemory(key, 'second');
        const view = render(Host, { tabs: [tabs[0]], ready: true, persistKey: 'routing-sections' });
        await fireEvent.click(view.getByRole('button', { name: 'First' }));
        await view.rerender({ tabs, ready: true });
        await waitFor(() => expect(view.getByTestId('active').textContent).toBe('first'));
        expect(readTabMemory(key, ['first', 'second'])).toBe('first');
    });

    it('explicit URLs take priority over remembered choices', async () => {
        writeTabMemory(key, 'second');
        location.set({ url: new URL('http://localhost/routing?tab=first'), route: { id: '/routing' } });
        const view = render(Host, { tabs, persistKey: 'routing-sections' });
        await waitFor(() => expect(view.getByTestId('active').textContent).toBe('first'));
        // The tab opened by the link is now the last open one.
        expect(readTabMemory(key, ['first', 'second'])).toBe('first');
    });

    it('persists an accepted user click', async () => {
        const view = render(Host, { tabs, persistKey: 'routing-sections' });
        await fireEvent.click(view.getByRole('button', { name: 'Second' }));
        await waitFor(() => expect(readTabMemory(key, ['first', 'second'])).toBe('second'));
    });

    it('accepts manual selection while optional status is still loading', async () => {
        const view = render(Host, { tabs, ready: false, persistKey: 'routing-sections' });
        await fireEvent.click(view.getByRole('button', { name: 'Second' }));
        await waitFor(() => expect(readTabMemory(key, ['first', 'second'])).toBe('second'));
    });

    it('does not persist rejected switches', async () => {
        const view = render(Host, { tabs, reject: true, persistKey: 'routing-sections' });
        await fireEvent.click(view.getByRole('button', { name: 'Second' }));
        await Promise.resolve();
        expect(view.getByTestId('active').textContent).toBe('first');
        expect(readTabMemory(key, ['first', 'second'])).toBe('first');
    });

    it('does not persist tab choices without an explicit key', async () => {
        const view = render(Host, { tabs });
        await fireEvent.click(view.getByRole('button', { name: 'Second' }));
        await Promise.resolve();
        expect(localStorage.length).toBe(0);
    });

    it('skips restoration for caller-specified deep-link params', async () => {
        writeTabMemory(key, 'second');
        location.set({ url: new URL('http://localhost/routing?policy=rule-1'), route: { id: '/routing' } });
        const view = render(Host, { tabs, persistKey: 'routing-sections', skipMemorySearchParams: ['policy'] });
        await waitFor(() => expect(view.getByTestId('active').textContent).toBe('first'));
    });

    it('skips restoration when the page prefers its own start tab', async () => {
        writeTabMemory(key, 'second');
        const view = render(Host, { tabs, persistKey: 'routing-sections', restoreMemory: false });
        await Promise.resolve();
        expect(view.getByTestId('active').textContent).toBe('first');
    });

    it('keeps the menu child when the group id changes with access level', async () => {
        const group = (children: { id: string; label: string }[]) => ({ id: children[0].id, label: 'Group', children });
        const a = { id: 'a', label: 'A' };
        const b = { id: 'b', label: 'B' };
        const view = render(Host, { tabs: [tabs[0], group([a, b])], persistKey: 'routing-sections' });
        const chip = () => view.getAllByRole('button').find((el) => el.getAttribute('aria-haspopup') === 'menu')!;
        await fireEvent.click(chip());
        await fireEvent.click(chip());
        await fireEvent.click(view.getByRole('button', { name: 'B' }));
        await fireEvent.click(view.getByRole('button', { name: 'First' }));
        await view.rerender({ tabs: [tabs[0], group([{ id: 'z', label: 'Z' }, a, b])] });
        await fireEvent.click(chip());
        await waitFor(() => expect(view.getByTestId('active').textContent).toBe('b'));
    });
});
