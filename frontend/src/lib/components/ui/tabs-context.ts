import { getContext, setContext } from 'svelte';

export interface TabsContext {
  active: () => string;
  setActive: (value: string) => void;
}

const KEY = Symbol('awg-tabs');

export function setTabsContext(ctx: TabsContext) {
  setContext(KEY, ctx);
}

export function getTabsContext(): TabsContext {
  const ctx = getContext<TabsContext | undefined>(KEY);
  if (!ctx) {
    throw new Error('<Tab> must be a descendant of <Tabs>');
  }
  return ctx;
}
