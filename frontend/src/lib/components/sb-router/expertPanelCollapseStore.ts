import { writable, type Readable } from 'svelte/store';

export type ExpertPanelSection =
  | 'rules'
  | 'ruleSets'
  | 'outbounds'
  | 'proxyGroups'
  | 'dnsServers'
  | 'dnsRewrite'
  | 'inbounds';

export type ExpertPanelCollapseState = Record<ExpertPanelSection, boolean>;

const STORAGE_KEY = 'awg.sb-router.expert-collapse';

const SECTIONS: ReadonlyArray<ExpertPanelSection> = [
  'rules',
  'ruleSets',
  'outbounds',
  'proxyGroups',
  'dnsServers',
  'dnsRewrite',
  'inbounds',
];

const DEFAULT_STATE: ExpertPanelCollapseState = {
  rules: false,
  ruleSets: false,
  outbounds: false,
  proxyGroups: false,
  dnsServers: false,
  dnsRewrite: false,
  inbounds: false,
};

function isSection(v: unknown): v is ExpertPanelSection {
  return typeof v === 'string' && (SECTIONS as readonly string[]).includes(v);
}

function readFromStorage(): ExpertPanelCollapseState {
  if (typeof window === 'undefined') return { ...DEFAULT_STATE };
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return { ...DEFAULT_STATE };
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== 'object') return { ...DEFAULT_STATE };
    const obj = parsed as Record<string, unknown>;
    const next = { ...DEFAULT_STATE };
    for (const key of SECTIONS) {
      if (typeof obj[key] === 'boolean') next[key] = obj[key];
    }
    return next;
  } catch {
    return { ...DEFAULT_STATE };
  }
}

function persist(state: ExpertPanelCollapseState): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(state));
  } catch {
    // private mode — ignore
  }
}

const store = writable<ExpertPanelCollapseState>(readFromStorage());

/** true — секция свёрнута (тело скрыто). */
export const expertPanelCollapse: Readable<ExpertPanelCollapseState> = { subscribe: store.subscribe };

export function isExpertPanelSectionCollapsed(
  state: ExpertPanelCollapseState,
  section: ExpertPanelSection,
): boolean {
  return state[section];
}

export function toggleExpertPanelSection(section: ExpertPanelSection): void {
  if (!isSection(section)) return;
  store.update((prev) => {
    const next = { ...prev, [section]: !prev[section] };
    persist(next);
    return next;
  });
}

export function setExpertPanelSectionCollapsed(section: ExpertPanelSection, collapsed: boolean): void {
  if (!isSection(section)) return;
  store.update((prev) => {
    if (prev[section] === collapsed) return prev;
    const next = { ...prev, [section]: collapsed };
    persist(next);
    return next;
  });
}
