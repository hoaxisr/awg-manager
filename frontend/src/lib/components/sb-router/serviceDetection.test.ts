import { describe, it, expect } from 'vitest';
import { detectServiceKey } from './serviceDetection';
import type { SingboxRouterRule, CatalogPreset } from '$lib/types';

function rule(partial: Partial<SingboxRouterRule>): SingboxRouterRule {
  return { ...partial };
}

const catalog: CatalogPreset[] = [
  {
    id: 'youtube',
    name: 'YouTube',
    iconSlug: 'youtube',
    category: 'social',
    origin: 'builtin',
    engines: {
      dns: { domains: ['youtube.com', 'ytimg.com', 'googlevideo.com', 'ytimg.l.google.com'] },
      singbox: { ruleSets: [{ tag: 'geosite-youtube', url: 'u' }], action: 'tunnel' },
    },
  },
  {
    id: 'google',
    name: 'Google',
    iconSlug: 'google',
    category: 'social',
    origin: 'builtin',
    engines: {
      dns: { domains: ['google.com', 'googleapis.com'] },
    },
  },
  {
    id: 'netflix',
    name: 'Netflix',
    iconSlug: 'netflix',
    category: 'social',
    origin: 'builtin',
    engines: {
      dns: { domains: ['netflix.com', 'nflxext.com'] },
    },
  },
  {
    id: 'telegram',
    name: 'Telegram',
    iconSlug: 'telegram',
    category: 'social',
    origin: 'builtin',
    engines: {
      dns: { domains: ['telegram.org', 't.me'] },
    },
  },
  {
    id: 'spotify',
    name: 'Spotify',
    iconSlug: 'spotify',
    category: 'social',
    origin: 'builtin',
    engines: {
      dns: { domains: ['spotify.com', 'scdn.co'] },
    },
  },
  {
    id: 'github',
    name: 'GitHub',
    iconSlug: 'github',
    category: 'social',
    origin: 'builtin',
    engines: {
      dns: { domains: ['github.com', 'githubusercontent.com'] },
    },
  },
  {
    id: 'openai',
    name: 'OpenAI',
    iconSlug: 'openai',
    category: 'ai',
    origin: 'builtin',
    engines: {
      dns: { domains: ['openai.com', 'chatgpt.com'] },
    },
  },
  {
    id: 'discord',
    name: 'Discord',
    iconSlug: 'discord',
    category: 'social',
    origin: 'builtin',
    engines: {
      dns: { domains: ['discord.com', 'discordapp.com'] },
    },
  },
  {
    id: 'twitch',
    name: 'Twitch',
    iconSlug: 'twitch',
    category: 'social',
    origin: 'builtin',
    engines: {
      dns: { domains: ['twitch.tv'] },
    },
  },
  {
    id: 'meta',
    name: 'Meta (все сервисы)',
    iconSlug: 'meta',
    category: 'social',
    origin: 'builtin',
    engines: {
      dns: { domains: ['facebook.com', 'instagram.com', 'whatsapp.com'] },
    },
  },
  {
    id: 'russian-services',
    name: 'Российские сервисы',
    iconSlug: 'rkn',
    category: 'block',
    origin: 'builtin',
    engines: {
      dns: { domains: ['yandex.ru'] },
    },
  },
];

describe('detectServiceKey', () => {
  it('returns "custom" for empty rule', () => {
    expect(detectServiceKey(rule({}), undefined, catalog)).toBe('custom');
  });

  it('detects netflix from domain_suffix', () => {
    expect(detectServiceKey(rule({ domain_suffix: ['netflix.com'] }), undefined, catalog)).toBe('netflix');
    expect(detectServiceKey(rule({ domain_suffix: ['nflxext.com'] }), undefined, catalog)).toBe('netflix');
  });

  it('detects youtube', () => {
    expect(detectServiceKey(rule({ domain_suffix: ['youtube.com'] }), undefined, catalog)).toBe('youtube');
    expect(detectServiceKey(rule({ domain_suffix: ['ytimg.com'] }), undefined, catalog)).toBe('youtube');
    expect(detectServiceKey(rule({ domain_suffix: ['googlevideo.com'] }), undefined, catalog)).toBe('youtube');
  });

  it('detects telegram', () => {
    expect(detectServiceKey(rule({ domain_suffix: ['telegram.org'] }), undefined, catalog)).toBe('telegram');
    expect(detectServiceKey(rule({ domain_suffix: ['t.me'] }), undefined, catalog)).toBe('telegram');
  });

  it('detects spotify', () => {
    expect(detectServiceKey(rule({ domain_suffix: ['spotify.com'] }), undefined, catalog)).toBe('spotify');
    expect(detectServiceKey(rule({ domain_suffix: ['scdn.co'] }), undefined, catalog)).toBe('spotify');
  });

  it('detects github', () => {
    expect(detectServiceKey(rule({ domain_suffix: ['github.com'] }), undefined, catalog)).toBe('github');
    expect(detectServiceKey(rule({ domain_suffix: ['githubusercontent.com'] }), undefined, catalog)).toBe('github');
  });

  it('detects openai (openai.com/chatgpt.com → preset id "openai")', () => {
    expect(detectServiceKey(rule({ domain_suffix: ['openai.com'] }), undefined, catalog)).toBe('openai');
    expect(detectServiceKey(rule({ domain_suffix: ['chatgpt.com'] }), undefined, catalog)).toBe('openai');
  });

  it('detects discord', () => {
    expect(detectServiceKey(rule({ domain_suffix: ['discord.com'] }), undefined, catalog)).toBe('discord');
    expect(detectServiceKey(rule({ domain_suffix: ['discordapp.com'] }), undefined, catalog)).toBe('discord');
  });

  it('detects twitch', () => {
    expect(detectServiceKey(rule({ domain_suffix: ['twitch.tv'] }), undefined, catalog)).toBe('twitch');
  });

  it('detects meta (facebook/instagram/whatsapp → preset id "meta")', () => {
    expect(detectServiceKey(rule({ domain_suffix: ['facebook.com'] }), undefined, catalog)).toBe('meta');
    expect(detectServiceKey(rule({ domain_suffix: ['instagram.com'] }), undefined, catalog)).toBe('meta');
    expect(detectServiceKey(rule({ domain_suffix: ['whatsapp.com'] }), undefined, catalog)).toBe('meta');
  });

  it('returns "custom" for apple domains (no apple preset in catalog)', () => {
    expect(detectServiceKey(rule({ domain_suffix: ['apple.com'] }), undefined, catalog)).toBe('custom');
    expect(detectServiceKey(rule({ domain_suffix: ['icloud.com'] }), undefined, catalog)).toBe('custom');
  });

  it('detects russian-services from rule_set geoip-ru / geosite-ru', () => {
    expect(detectServiceKey(rule({ rule_set: ['geoip-ru'] }), undefined, catalog)).toBe('rkn');
    expect(detectServiceKey(rule({ rule_set: ['geosite-ru'] }), undefined, catalog)).toBe('rkn');
  });

  it('domain_suffix takes precedence over rule_set when rule_set is empty', () => {
    expect(detectServiceKey(rule({
      domain_suffix: ['netflix.com'],
      rule_set: [],
    }), undefined, catalog)).toBe('netflix');
  });

  it('rule_set takes precedence over domain_suffix (geosite-youtube beats googleapis.com)', () => {
    const presets = [{
      id: 'youtube',
      name: 'YouTube',
      iconSlug: 'youtube',
      ruleSets: [{ tag: 'geosite-youtube', url: 'https://example/youtube.srs' }],
      rules: [{ ruleSetRef: 'geosite-youtube', actionTarget: 'tunnel' as const }],
    }];
    expect(detectServiceKey(rule({
      rule_set: ['geosite-youtube'],
      domain_suffix: ['googleapis.com'],
    }), presets, catalog)).toBe('youtube');
  });

  it('detects youtube for ytimg.l.google.com (longest suffix, not bare google.com)', () => {
    // youtube has 'ytimg.l.google.com' in catalog fixture — longer than google.com
    expect(detectServiceKey(rule({ domain_suffix: ['ytimg.l.google.com'] }), undefined, catalog)).toBe('youtube');
  });

  it('detects from rule_set tag without geosite- prefix', () => {
    const presets = [{
      id: 'netflix',
      name: 'Netflix',
      iconSlug: 'netflix',
      ruleSets: [{ tag: 'netflix-remote', url: 'https://example/netflix.srs' }],
      rules: [{ ruleSetRef: 'netflix-remote', actionTarget: 'tunnel' as const }],
    }];
    expect(detectServiceKey(rule({ rule_set: ['netflix-remote'] }), presets, catalog)).toBe('netflix');
  });

  it('detects from bare preset id rule_set without geosite- via catalog', () => {
    expect(detectServiceKey(rule({ rule_set: ['telegram'] }), undefined, catalog)).toBe('telegram');
  });

  it('falls back to "custom" for unknown domain', () => {
    expect(detectServiceKey(rule({ domain_suffix: ['example.com'] }), undefined, catalog)).toBe('custom');
  });

  it('handles undefined/null arrays gracefully', () => {
    expect(detectServiceKey(rule({ domain_suffix: undefined }), undefined, catalog)).toBe('custom');
  });

  it('handles empty arrays', () => {
    expect(detectServiceKey(rule({ domain_suffix: [], rule_set: [] }), undefined, catalog)).toBe('custom');
  });

  it('detects netflix from geosite-netflix rule_set via router presets', () => {
    const presets = [{
      id: 'netflix',
      name: 'Netflix',
      iconSlug: 'netflix',
      ruleSets: [{ tag: 'geosite-netflix', url: 'https://example/netflix.srs' }],
      rules: [{ ruleSetRef: 'geosite-netflix', actionTarget: 'tunnel' as const }],
    }];
    expect(detectServiceKey(rule({ rule_set: ['geosite-netflix'] }), presets, catalog)).toBe('netflix');
  });

  it('detects openai from geosite-openai (id mismatch with catalog chatgpt)', () => {
    const presets = [{
      id: 'openai',
      name: 'OpenAI',
      iconSlug: 'openai',
      ruleSets: [{ tag: 'geosite-openai', url: 'https://example/openai.srs' }],
      rules: [{ ruleSetRef: 'geosite-openai', actionTarget: 'tunnel' as const }],
    }];
    expect(detectServiceKey(rule({ rule_set: ['geosite-openai'] }), presets, catalog)).toBe('openai');
  });

  it('detects gemini from geosite-google-gemini legacy tag', () => {
    const presets = [{
      id: 'gemini',
      name: 'Gemini',
      iconSlug: 'googlegemini',
      ruleSets: [{ tag: 'geosite-google-gemini', url: 'https://example/gemini.srs' }],
      rules: [{ ruleSetRef: 'geosite-google-gemini', actionTarget: 'tunnel' as const }],
    }];
    expect(detectServiceKey(rule({ rule_set: ['geosite-google-gemini'] }), presets, catalog)).toBe('googlegemini');
  });

  it('returns custom for geosite rule_set without router presets and no catalog match', () => {
    expect(detectServiceKey(rule({ rule_set: ['geosite-unknownservice'] }), undefined, catalog)).toBe('custom');
  });
});
