import { describe, it, expect } from 'vitest';
import type { SingboxRouterOutbound } from '$lib/types';
import {
  buildWizardCompositeOutbound,
  findMatchingComposite,
  formatTunnelOutboundPreview,
  formatWizardOutboundPreview,
  nextCustomCompositeTag,
  previewTunnelOutboundResolution,
  sameOutboundMemberSet,
} from './wizardCompositeOutbound';

describe('sameOutboundMemberSet', () => {
  it('сравнивает наборы без учёта порядка', () => {
    expect(sameOutboundMemberSet(['a', 'b'], ['b', 'a'])).toBe(true);
    expect(sameOutboundMemberSet(['a'], ['a', 'b'])).toBe(false);
  });
});

describe('findMatchingComposite', () => {
  const outbounds: SingboxRouterOutbound[] = [
    { type: 'selector', tag: 'custom-composite-1', outbounds: ['warp', 'awg10'] },
    { type: 'urltest', tag: 'fast', outbounds: ['awg10', 'awg20'] },
    { type: 'direct', tag: 'direct-home', bind_interface: 'eth0' },
  ];

  it('находит composite с тем же набором', () => {
    expect(findMatchingComposite(outbounds, ['awg10', 'warp'])?.tag).toBe('custom-composite-1');
  });

  it('находит composite с тем же набором и типом', () => {
    expect(findMatchingComposite(outbounds, ['awg10', 'warp'], 'selector')?.tag).toBe('custom-composite-1');
    expect(findMatchingComposite(outbounds, ['awg10', 'warp'], 'urltest')).toBeUndefined();
  });

  it('не находит при другом составе', () => {
    expect(findMatchingComposite(outbounds, ['warp'])).toBeUndefined();
  });

  it('для fallback учитывает строгий порядок участников', () => {
    const fallbackOutbounds: SingboxRouterOutbound[] = [
      { type: 'fallback', tag: 'fb-group', outbounds: ['warp', 'awg10'] },
    ];
    // Exact order matches
    expect(findMatchingComposite(fallbackOutbounds, ['warp', 'awg10'], 'fallback')?.tag).toBe('fb-group');
    // Inverted order does NOT match because priority differs
    expect(findMatchingComposite(fallbackOutbounds, ['awg10', 'warp'], 'fallback')).toBeUndefined();
  });
});

describe('nextCustomCompositeTag', () => {
  it('первый свободный custom-composite-N', () => {
    expect(nextCustomCompositeTag([])).toBe('custom-composite-1');
    expect(nextCustomCompositeTag(['custom-composite-1', 'warp'])).toBe('custom-composite-2');
    expect(nextCustomCompositeTag(['custom-composite-2'])).toBe('custom-composite-1');
  });
});

describe('previewTunnelOutboundResolution', () => {
  const outbounds: SingboxRouterOutbound[] = [
    { type: 'selector', tag: 'custom-composite-1', outbounds: ['warp', 'awg10'] },
  ];

  it('один туннель — без composite', () => {
    expect(previewTunnelOutboundResolution(['warp'], outbounds)).toEqual({
      outboundTag: 'warp',
      willCreate: false,
      tunnelCount: 1,
      mode: 'urltest',
    });
  });

  it('существующий composite — без создания', () => {
    expect(previewTunnelOutboundResolution(['warp', 'awg10'], outbounds, 'selector')).toEqual({
      outboundTag: 'custom-composite-1',
      willCreate: false,
      tunnelCount: 2,
      mode: 'selector',
    });
  });

  it('новый набор — предложит создать custom-composite-2', () => {
    expect(previewTunnelOutboundResolution(['warp', 'awg20'], outbounds, 'fallback')).toEqual({
      outboundTag: 'custom-composite-2',
      willCreate: true,
      tunnelCount: 2,
      mode: 'fallback',
    });
  });
});

describe('formatTunnelOutboundPreview', () => {
  it('описывает один туннель, reuse и создание composite', () => {
    expect(
      formatTunnelOutboundPreview({ outboundTag: 'warp', willCreate: false, tunnelCount: 1, mode: 'urltest' }),
    ).toContain('«warp»');
    expect(
      formatTunnelOutboundPreview({
        outboundTag: 'custom-composite-1',
        willCreate: false,
        tunnelCount: 2,
        mode: 'fallback',
      }),
    ).toBe('Будет использован composite outbound «custom-composite-1» из выбранных туннелей (2) — резерв с переключением');
    expect(
      formatTunnelOutboundPreview({
        outboundTag: 'custom-composite-2',
        willCreate: true,
        tunnelCount: 2,
        mode: 'selector',
      }),
    ).toBe('Будет создан composite outbound «custom-composite-2» из выбранных туннелей (2) — ручной выбор');
  });
});

describe('formatWizardOutboundPreview', () => {
  it('direct и block не зависят от tunnel preview', () => {
    expect(formatWizardOutboundPreview('direct', null)).toContain('напрямую');
    expect(formatWizardOutboundPreview('block', null)).toContain('заблокирован');
  });
});

describe('buildWizardCompositeOutbound', () => {
  it('создаёт urltest с дефолтными параметрами проб', () => {
    expect(buildWizardCompositeOutbound('custom-composite-1', ['warp', 'awg10'], 'urltest')).toEqual({
      type: 'urltest',
      tag: 'custom-composite-1',
      outbounds: ['warp', 'awg10'],
      url: 'https://www.gstatic.com/generate_204',
      interval: '60s',
      tolerance: 50,
    });
  });

  it('создаёт fallback с interrupt_exist_connections', () => {
    expect(buildWizardCompositeOutbound('custom-composite-1', ['warp', 'awg10'], 'fallback')).toEqual({
      type: 'fallback',
      tag: 'custom-composite-1',
      outbounds: ['warp', 'awg10'],
      url: 'https://www.gstatic.com/generate_204',
      interval: '60s',
      interrupt_exist_connections: true,
    });
  });

  it('создаёт selector с дефолтным первым участником', () => {
    expect(buildWizardCompositeOutbound('custom-composite-1', ['warp', 'awg10'], 'selector')).toEqual({
      type: 'selector',
      tag: 'custom-composite-1',
      outbounds: ['warp', 'awg10'],
      default: 'warp',
    });
  });
});
