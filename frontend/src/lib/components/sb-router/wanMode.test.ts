import { describe, expect, it } from 'vitest';
import { resolveWanAuto, planToggleAutoDetect, planSelectWanInterface } from './wanMode';

describe('resolveWanAuto', () => {
  it('следует стору, когда оверрайда нет', () => {
    expect(resolveWanAuto(null, true)).toBe(true);
    expect(resolveWanAuto(null, false)).toBe(false);
  });
  it('по умолчанию авто, если стор ещё не загружен', () => {
    expect(resolveWanAuto(null, undefined)).toBe(true);
  });
  it('оверрайд false перекрывает persisted-авто (показываем селектор)', () => {
    expect(resolveWanAuto(false, true)).toBe(false);
  });
});

describe('planToggleAutoDetect', () => {
  it('включение авто персистит сброс интерфейса и снимает оверрайд', () => {
    expect(planToggleAutoDetect(true)).toEqual({
      override: null,
      patch: { wanAutoDetect: true, wanInterface: '' },
    });
  });
  it('выключение авто НЕ персистит — только показывает селектор', () => {
    expect(planToggleAutoDetect(false)).toEqual({ override: false, patch: null });
  });
});

describe('planSelectWanInterface', () => {
  it('выбор интерфейса персистит ручной режим', () => {
    expect(planSelectWanInterface('ppp0')).toEqual({
      override: null,
      patch: { wanAutoDetect: false, wanInterface: 'ppp0' },
    });
  });
  it('пустой выбор («— выберите —») — no-op', () => {
    expect(planSelectWanInterface('')).toBeNull();
  });
});
