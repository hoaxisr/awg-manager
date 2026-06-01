import { describe, it, expect } from 'vitest';
import { detectServiceKey } from './serviceDetection';
import type { SingboxRouterRule } from '$lib/types';

function rule(partial: Partial<SingboxRouterRule>): SingboxRouterRule {
  return { ...partial };
}

describe('detectServiceKey', () => {
  it('returns "custom" for empty rule', () => {
    expect(detectServiceKey(rule({}))).toBe('custom');
  });

  it('detects netflix from domain_suffix', () => {
    expect(detectServiceKey(rule({ domain_suffix: ['netflix.com'] }))).toBe('netflix');
    expect(detectServiceKey(rule({ domain_suffix: ['nflxext.com'] }))).toBe('netflix');
  });

  it('detects youtube', () => {
    expect(detectServiceKey(rule({ domain_suffix: ['youtube.com'] }))).toBe('youtube');
    expect(detectServiceKey(rule({ domain_suffix: ['ytimg.com'] }))).toBe('youtube');
    expect(detectServiceKey(rule({ domain_suffix: ['googlevideo.com'] }))).toBe('youtube');
  });

  it('detects telegram', () => {
    expect(detectServiceKey(rule({ domain_suffix: ['telegram.org'] }))).toBe('telegram');
    expect(detectServiceKey(rule({ domain_suffix: ['t.me'] }))).toBe('telegram');
  });

  it('detects spotify', () => {
    expect(detectServiceKey(rule({ domain_suffix: ['spotify.com'] }))).toBe('spotify');
    expect(detectServiceKey(rule({ domain_suffix: ['scdn.co'] }))).toBe('spotify');
  });

  it('detects github', () => {
    expect(detectServiceKey(rule({ domain_suffix: ['github.com'] }))).toBe('github');
    expect(detectServiceKey(rule({ domain_suffix: ['githubusercontent.com'] }))).toBe('github');
  });

  it('detects chatgpt (openai.com/chatgpt.com → preset id "chatgpt")', () => {
    // SERVICE_PRESETS has id 'chatgpt' covering openai.com and chatgpt.com
    expect(detectServiceKey(rule({ domain_suffix: ['openai.com'] }))).toBe('chatgpt');
    expect(detectServiceKey(rule({ domain_suffix: ['chatgpt.com'] }))).toBe('chatgpt');
  });

  it('detects discord', () => {
    expect(detectServiceKey(rule({ domain_suffix: ['discord.com'] }))).toBe('discord');
    expect(detectServiceKey(rule({ domain_suffix: ['discordapp.com'] }))).toBe('discord');
  });

  it('detects twitch', () => {
    expect(detectServiceKey(rule({ domain_suffix: ['twitch.tv'] }))).toBe('twitch');
  });

  it('detects social (facebook/instagram/whatsapp → preset id "social")', () => {
    // SERVICE_PRESETS has id 'social' — no separate 'meta' preset exists
    expect(detectServiceKey(rule({ domain_suffix: ['facebook.com'] }))).toBe('social');
    expect(detectServiceKey(rule({ domain_suffix: ['instagram.com'] }))).toBe('social');
    expect(detectServiceKey(rule({ domain_suffix: ['whatsapp.com'] }))).toBe('social');
  });

  it('returns "custom" for apple domains (no apple preset in SERVICE_PRESETS)', () => {
    expect(detectServiceKey(rule({ domain_suffix: ['apple.com'] }))).toBe('custom');
    expect(detectServiceKey(rule({ domain_suffix: ['icloud.com'] }))).toBe('custom');
  });

  it('detects russian-services from rule_set geoip-ru / geosite-ru', () => {
    expect(detectServiceKey(rule({ rule_set: ['geoip-ru'] }))).toBe('russian-services');
    expect(detectServiceKey(rule({ rule_set: ['geosite-ru'] }))).toBe('russian-services');
  });

  it('domain_suffix takes precedence over rule_set when rule_set is empty', () => {
    expect(detectServiceKey(rule({
      domain_suffix: ['netflix.com'],
      rule_set: [],
    }))).toBe('netflix');
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
    }), presets)).toBe('youtube');
  });

  it('detects youtube for ytimg.l.google.com (longest suffix, not bare google.com)', () => {
    expect(detectServiceKey(rule({ domain_suffix: ['ytimg.l.google.com'] }))).toBe('youtube');
  });

  it('detects from rule_set tag without geosite- prefix', () => {
    const presets = [{
      id: 'netflix',
      name: 'Netflix',
      iconSlug: 'netflix',
      ruleSets: [{ tag: 'netflix-remote', url: 'https://example/netflix.srs' }],
      rules: [{ ruleSetRef: 'netflix-remote', actionTarget: 'tunnel' as const }],
    }];
    expect(detectServiceKey(rule({ rule_set: ['netflix-remote'] }), presets)).toBe('netflix');
  });

  it('detects from bare preset id rule_set without geosite- via SERVICE_PRESETS', () => {
    expect(detectServiceKey(rule({ rule_set: ['telegram'] }))).toBe('telegram');
  });

  it('falls back to "custom" for unknown domain', () => {
    expect(detectServiceKey(rule({ domain_suffix: ['example.com'] }))).toBe('custom');
  });

  it('handles undefined/null arrays gracefully', () => {
    expect(detectServiceKey(rule({ domain_suffix: undefined }))).toBe('custom');
  });

  it('handles empty arrays', () => {
    expect(detectServiceKey(rule({ domain_suffix: [], rule_set: [] }))).toBe('custom');
  });

  it('detects netflix from geosite-netflix rule_set via router presets', () => {
    const presets = [{
      id: 'netflix',
      name: 'Netflix',
      iconSlug: 'netflix',
      ruleSets: [{ tag: 'geosite-netflix', url: 'https://example/netflix.srs' }],
      rules: [{ ruleSetRef: 'geosite-netflix', actionTarget: 'tunnel' as const }],
    }];
    expect(detectServiceKey(rule({ rule_set: ['geosite-netflix'] }), presets)).toBe('netflix');
  });

  it('detects openai from geosite-openai (id mismatch with SERVICE_PRESETS chatgpt)', () => {
    const presets = [{
      id: 'openai',
      name: 'OpenAI',
      iconSlug: 'openai',
      ruleSets: [{ tag: 'geosite-openai', url: 'https://example/openai.srs' }],
      rules: [{ ruleSetRef: 'geosite-openai', actionTarget: 'tunnel' as const }],
    }];
    expect(detectServiceKey(rule({ rule_set: ['geosite-openai'] }), presets)).toBe('openai');
  });

  it('detects gemini from geosite-google-gemini legacy tag', () => {
    const presets = [{
      id: 'gemini',
      name: 'Gemini',
      iconSlug: 'googlegemini',
      ruleSets: [{ tag: 'geosite-google-gemini', url: 'https://example/gemini.srs' }],
      rules: [{ ruleSetRef: 'geosite-google-gemini', actionTarget: 'tunnel' as const }],
    }];
    expect(detectServiceKey(rule({ rule_set: ['geosite-google-gemini'] }), presets)).toBe('googlegemini');
  });

  it('returns custom for geosite rule_set without router presets', () => {
    expect(detectServiceKey(rule({ rule_set: ['geosite-openai'] }))).toBe('custom');
  });
});
