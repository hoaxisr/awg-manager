import { describe, it, expect, vi } from 'vitest';
import type { SingboxQosClass } from '$lib/types';
import {
  QOS_MAX_CLASSES,
  clampDscp,
  normalizeQosClasses,
  nextFreeDscp,
  addQosClass,
  isDscpTaken,
  updateQosClass,
  removeQosClass,
  resolveOutboundOptions,
  createSaveQueue,
  isQosLockedByMode,
} from './qosClasses';

function cls(dscp: number, over: Partial<SingboxQosClass> = {}): SingboxQosClass {
  return { dscp, name: `c${dscp}`, outbound: 'direct', enabled: true, ...over };
}

describe('clampDscp', () => {
  it('clamps to 0–63 and truncates fractions', () => {
    expect(clampDscp(-5)).toBe(0);
    expect(clampDscp(64)).toBe(63);
    expect(clampDscp(46.9)).toBe(46);
    expect(clampDscp(NaN)).toBe(0);
    expect(clampDscp(Infinity)).toBe(0);
  });
});

describe('normalizeQosClasses', () => {
  it('treats undefined/null (mock payloads without the field) as empty list', () => {
    expect(normalizeQosClasses(undefined)).toEqual([]);
    expect(normalizeQosClasses(null)).toEqual([]);
  });

  it('dedupes duplicate dscp values, first occurrence wins', () => {
    const out = normalizeQosClasses([cls(46, { name: 'first' }), cls(46, { name: 'second' }), cls(8)]);
    expect(out.map((c) => c.dscp)).toEqual([46, 8]);
    expect(out[0].name).toBe('first');
  });

  it('caps the list at 8 classes', () => {
    const input = Array.from({ length: 12 }, (_, i) => cls(i));
    const out = normalizeQosClasses(input);
    expect(out).toHaveLength(QOS_MAX_CLASSES);
    expect(out.map((c) => c.dscp)).toEqual([0, 1, 2, 3, 4, 5, 6, 7]);
  });

  it('clamps dscp and truncates name to 32 chars', () => {
    const out = normalizeQosClasses([cls(200, { name: 'x'.repeat(40) })]);
    expect(out[0].dscp).toBe(63);
    expect(out[0].name).toHaveLength(32);
  });

  it('dedupe applies after clamping (100 and 63 collide)', () => {
    const out = normalizeQosClasses([cls(100), cls(63)]);
    expect(out).toHaveLength(1);
    expect(out[0].dscp).toBe(63);
  });

  it('defaults enabled to true unless explicitly false', () => {
    const out = normalizeQosClasses([
      { dscp: 1, name: 'a', outbound: 'direct', enabled: undefined as unknown as boolean },
      cls(2, { enabled: false }),
    ]);
    expect(out[0].enabled).toBe(true);
    expect(out[1].enabled).toBe(false);
  });
});

describe('nextFreeDscp / addQosClass', () => {
  it('prefers presets 46 → 32 → 8, then lowest free value', () => {
    expect(nextFreeDscp([])).toBe(46);
    expect(nextFreeDscp([cls(46)])).toBe(32);
    expect(nextFreeDscp([cls(46), cls(32)])).toBe(8);
    expect(nextFreeDscp([cls(46), cls(32), cls(8), cls(0)])).toBe(1);
  });

  it('addQosClass appends an enabled class with a free dscp', () => {
    const out = addQosClass([cls(46)], 'awg-vpn0');
    expect(out).not.toBeNull();
    expect(out).toHaveLength(2);
    expect(out![1]).toMatchObject({ dscp: 32, outbound: 'awg-vpn0', enabled: true });
  });

  it('addQosClass returns null at the 8-class cap', () => {
    const full = Array.from({ length: QOS_MAX_CLASSES }, (_, i) => cls(i));
    expect(addQosClass(full, 'direct')).toBeNull();
  });
});

describe('isDscpTaken', () => {
  it('detects duplicates excluding the edited row itself', () => {
    const list = [cls(46), cls(8)];
    expect(isDscpTaken(list, 0, 46)).toBe(false); // own value
    expect(isDscpTaken(list, 0, 8)).toBe(true); // other row's value
    expect(isDscpTaken(list, 1, 32)).toBe(false);
  });
});

describe('updateQosClass / removeQosClass', () => {
  it('patches one entry immutably with clamping and name truncation', () => {
    const list = [cls(46), cls(8)];
    const out = updateQosClass(list, 1, { dscp: 99, name: 'y'.repeat(50) });
    expect(out[1].dscp).toBe(63);
    expect(out[1].name).toHaveLength(32);
    expect(out[0]).toBe(list[0]); // untouched entry keeps identity
    expect(list[1].dscp).toBe(8); // source not mutated
  });

  it('removes by index', () => {
    const out = removeQosClass([cls(46), cls(8)], 0);
    expect(out.map((c) => c.dscp)).toEqual([8]);
  });
});

describe('resolveOutboundOptions', () => {
  const opts = [
    { value: 'direct', label: 'Direct' },
    { value: 'awg-vpn0', label: 'VPN 0' },
  ];

  it('returns options as-is when the tag is present', () => {
    const out = resolveOutboundOptions(opts, 'awg-vpn0');
    expect(out.options).toBe(opts);
    expect(out.missing).toBe(false);
  });

  it('returns options as-is for an empty tag (placeholder is legit)', () => {
    const out = resolveOutboundOptions(opts, '');
    expect(out.options).toBe(opts);
    expect(out.missing).toBe(false);
  });

  it('cold-load (no options yet): shows the raw tag, no missing warning', () => {
    const out = resolveOutboundOptions([], 'awg-gone');
    expect(out.options).toEqual([{ value: 'awg-gone', label: 'awg-gone', disabled: true }]);
    expect(out.missing).toBe(false);
  });

  it('missing tag among real options: appends disabled «(недоступен)» entry and flags missing', () => {
    const out = resolveOutboundOptions(opts, 'awg-gone');
    expect(out.missing).toBe(true);
    expect(out.options).toHaveLength(3);
    expect(out.options[2]).toEqual({
      value: 'awg-gone',
      label: '(недоступен) awg-gone',
      disabled: true,
    });
  });
});

describe('createSaveQueue', () => {
  function deferred() {
    let resolve!: () => void;
    let reject!: (e: unknown) => void;
    const promise = new Promise<void>((res, rej) => {
      resolve = res;
      reject = rej;
    });
    return { promise, resolve, reject };
  }
  const tick = () => new Promise<void>((r) => setTimeout(r, 0));

  it('serializes saves: second commit waits for the first PUT to finish', async () => {
    const gates = [deferred(), deferred()];
    let calls = 0;
    const save = vi.fn((_: number) => gates[calls++].promise);
    const onDrained = vi.fn();
    const enqueue = createSaveQueue<number>(save, onDrained);

    enqueue(1);
    await tick();
    enqueue(2);
    await tick();
    expect(save).toHaveBeenCalledTimes(1); // 2 queued, not sent yet

    gates[0].resolve();
    await tick();
    expect(save).toHaveBeenCalledTimes(2);
    expect(save).toHaveBeenLastCalledWith(2);
    expect(onDrained).not.toHaveBeenCalled();

    gates[1].resolve();
    await tick();
    expect(onDrained).toHaveBeenCalledTimes(1);
  });

  it('coalesces commits made while a save is in flight (latest snapshot wins)', async () => {
    const gate = deferred();
    const seen: number[] = [];
    const save = vi.fn((v: number) => {
      seen.push(v);
      return seen.length === 1 ? gate.promise : Promise.resolve();
    });
    const enqueue = createSaveQueue<number>(save, () => {});

    enqueue(1);
    enqueue(2);
    enqueue(3);
    gate.resolve();
    await tick();
    expect(seen).toEqual([1, 3]); // 2 superseded by 3 while 1 was in flight
  });

  it('keeps draining after a failed save and still calls onDrained', async () => {
    let calls = 0;
    const save = vi.fn((_: number) =>
      calls++ === 0 ? Promise.reject(new Error('boom')) : Promise.resolve(),
    );
    const onDrained = vi.fn();
    const enqueue = createSaveQueue<number>(save, onDrained);

    enqueue(1);
    await tick();
    expect(onDrained).toHaveBeenCalledTimes(1);

    enqueue(2);
    await tick();
    expect(save).toHaveBeenCalledTimes(2);
    expect(onDrained).toHaveBeenCalledTimes(2);
  });
});

// Классы DSCP опираются на netfilter-перехват, которого в fakeip-tun нет.
// Предикат один на два места: QosSettingsCard гейтит им РЕНДЕР таблицы,
// EngineQosCard — прайминг каталога outbound'ов. Разъехавшись, они дали бы
// пустой дропдаун ровно там, ради чего прайминг и заводили.
describe('isQosLockedByMode', () => {
  it('fakeip-tun запирает карточку', () => {
    expect(isQosLockedByMode('fakeip-tun')).toBe(true);
  });

  it('tproxy и policy-tun не запирают', () => {
    expect(isQosLockedByMode('tproxy')).toBe(false);
    expect(isQosLockedByMode('policy-tun')).toBe(false);
  });

  // Легаси-payload без routingMode = tproxy (тот же дефолт, что у бэкенда).
  it('отсутствующий режим не запирает', () => {
    expect(isQosLockedByMode(undefined)).toBe(false);
    expect(isQosLockedByMode(null)).toBe(false);
    expect(isQosLockedByMode('')).toBe(false);
  });

  it('незнакомый режим не запирает', () => {
    expect(isQosLockedByMode('что-то новое')).toBe(false);
  });
});
