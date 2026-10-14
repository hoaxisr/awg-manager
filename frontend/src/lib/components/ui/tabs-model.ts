// Pure tab-row logic for Tabs.svelte: group flattening, fit measurement, hints.

export type TabBadgeTone = 'default' | 'success' | 'warning' | 'muted';

export interface TabChild {
    id: string;
    label: string;
    badge?: number | string;
    badgeTone?: TabBadgeTone;
}

export interface Tab {
    id: string;
    label: string;
    badge?: number | string;
    // Optional visual tone for status-style string badges. Numbers keep
    // the neutral default. `success` = green, `warning` = amber,
    // `muted` = subdued grey (e.g. "stopped").
    badgeTone?: TabBadgeTone;
    // When true, renders a small vertical divider + extra spacing before
    // this tab. Used to visually group tabs into clusters (e.g. legacy
    // NDMS-stack tabs vs sing-box stack on /routing).
    separatorBefore?: boolean;
    // Dropdown group: one chip in the row, children picked from a menu.
    // `id` is the default child when activating the group. Active when any
    // child id matches `active`. With a single child, renders as a plain tab.
    // When the whole row fits flattened, children render as separate chips —
    // so child ids must be unique across the whole row, not just the group.
    children?: TabChild[];
}

export function leafIds(tab: Tab): string[] {
    if (tab.children && tab.children.length > 0) {
        return tab.children.map((c) => c.id);
    }
    return [tab.id];
}

export function isMenuTab(tab: Tab): boolean {
    return (tab.children?.length ?? 0) > 1;
}

// Menu groups become one plain tab per child, labelled "Group · Child".
// Single-child groups collapse to their child; plain tabs pass through.
export function flattenTabs(tabs: Tab[]): Tab[] {
    return tabs.flatMap((tab) => {
        if (!isMenuTab(tab)) {
            const leaf = tab.children?.[0];
            if (!leaf) return [tab];
            return [{
                id: leaf.id,
                label: leaf.label,
                badge: leaf.badge ?? tab.badge,
                badgeTone: leaf.badgeTone ?? tab.badgeTone,
                separatorBefore: tab.separatorBefore,
            }];
        }
        return tab.children!.map((c, i) => ({
            id: c.id,
            label: `${tab.label} · ${c.label}`,
            badge: c.badge,
            badgeTone: c.badgeTone,
            separatorBefore: i === 0 ? tab.separatorBefore : undefined,
        }));
    });
}

// True when a child other than the one shown on the collapsed chip has a
// non-zero count — the group hides content the user can't see from the row.
export function hasHiddenItems(tab: Tab, shownChildId: string | undefined): boolean {
    return (tab.children ?? []).some(
        (c) => c.id !== shownChildId && typeof c.badge === 'number' && c.badge > 0,
    );
}

export interface MeasuredItem {
    width: number;
    separator: boolean;
}

// How many tabs fit in containerWidth. Every tab except the last reserves
// room for the "+N" overflow chip; a separator is paid for only together
// with the tab that follows it. Returns at least 1.
export function countFitting(items: MeasuredItem[], containerWidth: number, chipWidth: number): number {
    const totalTabs = items.filter((i) => !i.separator).length;
    let usedWidth = 0;
    let tabsFit = 0;
    let pendingSeparator = 0;
    for (const item of items) {
        if (item.separator) {
            pendingSeparator += item.width;
            continue;
        }
        const isLastTab = tabsFit === totalTabs - 1;
        const cost = item.width + pendingSeparator + (isLastTab ? 0 : chipWidth);
        if (usedWidth + cost > containerWidth) break;
        usedWidth += item.width + pendingSeparator;
        pendingSeparator = 0;
        tabsFit++;
    }
    return Math.max(1, tabsFit);
}
