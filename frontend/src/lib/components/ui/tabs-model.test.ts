import { describe, expect, it } from 'vitest';
import { countFitting, flattenTabs, hasHiddenItems, type Tab } from './tabs-model';

const singbox: Tab = {
    id: 'singbox',
    label: 'Sing-box',
    separatorBefore: true,
    children: [
        { id: 'singbox', label: 'Tunnels', badge: 0 },
        { id: 'subscriptions', label: 'Subscriptions', badge: 2 },
    ],
};

describe('flattenTabs', () => {
    it('expands a menu group into prefixed chips, separator on the first only', () => {
        const flat = flattenTabs([{ id: 'awg', label: 'AWG', badge: 3 }, singbox]);
        expect(flat).toEqual([
            { id: 'awg', label: 'AWG', badge: 3 },
            { id: 'singbox', label: 'Sing-box · Tunnels', badge: 0, separatorBefore: true },
            { id: 'subscriptions', label: 'Sing-box · Subscriptions', badge: 2 },
        ]);
    });

    it('collapses a single-child group to its child without a prefix', () => {
        const flat = flattenTabs([{ id: 'x', label: 'Group', separatorBefore: true, children: [{ id: 'leaf', label: 'Leaf', badge: 1 }] }]);
        expect(flat).toEqual([{ id: 'leaf', label: 'Leaf', badge: 1, separatorBefore: true }]);
    });
});

describe('hasHiddenItems', () => {
    it('is true when a non-shown child has a positive count', () => {
        expect(hasHiddenItems(singbox, 'singbox')).toBe(true);
    });

    it('ignores the shown child and zero or non-numeric badges', () => {
        expect(hasHiddenItems(singbox, 'subscriptions')).toBe(false);
        const g: Tab = { id: 'a', label: 'G', children: [{ id: 'a', label: 'A' }, { id: 'b', label: 'B', badge: 'on' }] };
        expect(hasHiddenItems(g, 'a')).toBe(false);
    });
});

describe('countFitting', () => {
    const tabs = [
        { width: 100, separator: false },
        { width: 10, separator: true },
        { width: 100, separator: false },
    ];

    it('fits everything when the last tab needs no overflow chip', () => {
        expect(countFitting(tabs, 210, 60)).toBe(2);
    });

    it('stops one short when the row is a pixel too narrow', () => {
        expect(countFitting(tabs, 209, 60)).toBe(1);
    });

    it('reserves room for the "+N" chip before every tab but the last', () => {
        const three = [100, 100, 100].map((width) => ({ width, separator: false }));
        expect(countFitting(three, 230, 60)).toBe(1);
    });

    it('never returns less than one', () => {
        expect(countFitting(tabs, 10, 60)).toBe(1);
    });
});
