import { describe, it, expect, beforeEach, vi } from 'vitest';
import { get } from 'svelte/store';
import { notifications } from './notifications';
import { notificationCenter } from './notificationCenter';

function createLocalStorageMock(): Storage {
  const data = new Map<string, string>();
  return {
    get length() { return data.size; },
    clear: () => data.clear(),
    getItem: (k: string) => data.get(k) ?? null,
    key: (i: number) => Array.from(data.keys())[i] ?? null,
    removeItem: (k: string) => { data.delete(k); },
    setItem: (k: string, v: string) => { data.set(k, v); },
  } as Storage;
}

beforeEach(() => {
  vi.stubGlobal('localStorage', createLocalStorageMock());
  notificationCenter.clearAll();
});

describe('notifications → notificationCenter mirroring', () => {
  it('mirrors error toasts into the center', () => {
    notifications.error('boom', { duration: 0 });
    const list = get(notificationCenter);
    expect(list).toHaveLength(1);
    expect(list[0]).toMatchObject({ type: 'error', message: 'boom' });
  });

  it('mirrors warning toasts into the center', () => {
    notifications.warning('careful', { duration: 0 });
    expect(get(notificationCenter)).toHaveLength(1);
  });

  it('does NOT mirror success or info toasts', () => {
    notifications.success('ok', { duration: 0 });
    notifications.info('fyi', { duration: 0 });
    expect(get(notificationCenter)).toHaveLength(0);
  });

  it('forwards the action to the center entry', () => {
    notifications.error('with link', { duration: 0, action: { label: 'Перейти', href: '/x' } });
    expect(get(notificationCenter)[0].action).toEqual({ label: 'Перейти', href: '/x' });
  });
});
