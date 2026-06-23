import { describe, it, expect, beforeEach, vi } from 'vitest';
import { get } from 'svelte/store';

function resetEnv(url: string) {
  window.history.replaceState({}, '', url);
  vi.resetModules();
}

describe('traceStore', () => {
  beforeEach(() => {
    resetEnv('/');
  });

  const dnsOk = {
    input: 'netflix.com',
    inputType: 'domain' as const,
    matches: [],
    matchedRule: -1,
    server: 'fakeip',
    classification: 'fakeip' as const,
    pool: '198.18.0.0/15',
    final: 'fakeip',
  };

  it('default state: closed, empty input, no result', async () => {
    const { traceOpen, traceInput, traceResult, traceError, traceLoading, dnsResult } = await import('./traceStore');
    expect(get(traceOpen)).toBe(false);
    expect(get(traceInput).domain).toBe('');
    expect(get(traceResult)).toBeNull();
    expect(get(traceError)).toBeNull();
    expect(get(traceLoading)).toBe(false);
    expect(get(dnsResult)).toBeNull();
  });

  it('init: URL ?trace=1 → traceOpen=true', async () => {
    resetEnv('/?tab=singbox&trace=1');
    const { traceOpen } = await import('./traceStore');
    expect(get(traceOpen)).toBe(true);
  });

  it('init: URL ?q=netflix.com → traceInput.domain', async () => {
    resetEnv('/?trace=1&q=netflix.com');
    const { traceOpen, traceInput } = await import('./traceStore');
    expect(get(traceOpen)).toBe(true);
    expect(get(traceInput).domain).toBe('netflix.com');
  });

  it('openTrace() → URL trace=1', async () => {
    const { openTrace, traceOpen } = await import('./traceStore');
    openTrace();
    expect(get(traceOpen)).toBe(true);
    expect(new URL(window.location.href).searchParams.get('trace')).toBe('1');
  });

  it('openTrace(domain) → URL trace=1 + q=domain', async () => {
    const { openTrace, traceInput } = await import('./traceStore');
    openTrace('youtube.com');
    expect(get(traceInput).domain).toBe('youtube.com');
    const sp = new URL(window.location.href).searchParams;
    expect(sp.get('trace')).toBe('1');
    expect(sp.get('q')).toBe('youtube.com');
  });

  it('closeTrace() удаляет trace+q из URL', async () => {
    resetEnv('/?tab=singbox&trace=1&q=netflix.com&other=keep');
    const { closeTrace, traceOpen } = await import('./traceStore');
    closeTrace();
    expect(get(traceOpen)).toBe(false);
    const sp = new URL(window.location.href).searchParams;
    expect(sp.get('trace')).toBeNull();
    expect(sp.get('q')).toBeNull();
    expect(sp.get('tab')).toBe('singbox');
    expect(sp.get('other')).toBe('keep');
  });

  it('runTrace() — happy path: loading→result', async () => {
    const mockResult = {
      input: 'netflix.com',
      inputType: 'domain' as const,
      matches: [{ index: 0, matched: true, action: 'route', outbound: 'warp' }],
      destination: 'warp',
      matchedRule: 0,
      final: 'direct',
    };
    vi.doMock('$lib/api/client', () => ({
      api: {
        singboxRouterInspectRoute: vi.fn().mockResolvedValue(mockResult),
        singboxRouterInspectDNS: vi.fn().mockResolvedValue(dnsOk),
      },
    }));
    const { runTrace, traceInput, traceResult, traceLoading, traceError, dnsResult, dnsError } = await import('./traceStore');
    traceInput.set({ domain: 'netflix.com' });

    const promise = runTrace();
    expect(get(traceLoading)).toBe(true);
    await promise;

    expect(get(traceLoading)).toBe(false);
    expect(get(traceResult)).toEqual(mockResult);
    expect(get(traceError)).toBeNull();
    expect(get(dnsResult)).toEqual(dnsOk);
    expect(get(dnsError)).toBeNull();
  });

  it('runTrace() — DNS error is isolated from route success', async () => {
    const mockResult = {
      input: 'netflix.com', inputType: 'domain' as const, matches: [],
      destination: 'warp', matchedRule: -1, final: 'direct',
    };
    vi.doMock('$lib/api/client', () => ({
      api: {
        singboxRouterInspectRoute: vi.fn().mockResolvedValue(mockResult),
        singboxRouterInspectDNS: vi.fn().mockRejectedValue(new Error('DNS down')),
      },
    }));
    const { runTrace, traceInput, traceResult, traceError, dnsResult, dnsError } = await import('./traceStore');
    traceInput.set({ domain: 'netflix.com' });

    await runTrace();
    expect(get(traceResult)).toEqual(mockResult);
    expect(get(traceError)).toBeNull();
    expect(get(dnsResult)).toBeNull();
    expect(get(dnsError)).toMatch(/DNS down/);
  });

  it('runTrace() передаёт queryType/sourceIP в DNS API', async () => {
    const dnsMock = vi.fn().mockResolvedValue(dnsOk);
    vi.doMock('$lib/api/client', () => ({
      api: {
        singboxRouterInspectRoute: vi.fn().mockResolvedValue({
          input: 'x', inputType: 'domain', matches: [], destination: 'direct', matchedRule: -1, final: 'direct',
        }),
        singboxRouterInspectDNS: dnsMock,
      },
    }));
    const { runTrace, traceInput } = await import('./traceStore');
    traceInput.set({ domain: 'discord.com', queryType: 'AAAA', sourceIP: '192.168.0.70' });

    await runTrace();
    expect(dnsMock).toHaveBeenCalledWith({ domain: 'discord.com', queryType: 'AAAA', sourceIP: '192.168.0.70' });
  });

  it('runTrace() — error path: API throws → traceError set', async () => {
    vi.doMock('$lib/api/client', () => ({
      api: {
        singboxRouterInspectRoute: vi.fn().mockRejectedValue(new Error('Network failed')),
        singboxRouterInspectDNS: vi.fn().mockResolvedValue(dnsOk),
      },
    }));
    const { runTrace, traceInput, traceError, traceLoading, traceResult } = await import('./traceStore');
    traceInput.set({ domain: 'netflix.com' });

    await runTrace();
    expect(get(traceLoading)).toBe(false);
    expect(get(traceError)).toMatch(/Network failed/);
    expect(get(traceResult)).toBeNull();
  });

  it('runTrace() — пустой domain → no API call, no error', async () => {
    const inspectMock = vi.fn();
    vi.doMock('$lib/api/client', () => ({
      api: { singboxRouterInspectRoute: inspectMock },
    }));
    const { runTrace, traceInput, traceError } = await import('./traceStore');
    traceInput.set({ domain: '' });

    await runTrace();
    expect(inspectMock).not.toHaveBeenCalled();
    expect(get(traceError)).toBeNull();
  });

  it('runTrace() передаёт port/protocol в API', async () => {
    const inspectMock = vi.fn().mockResolvedValue({
      input: 'netflix.com', inputType: 'domain', matches: [], destination: 'direct', matchedRule: -1, final: 'direct',
    });
    vi.doMock('$lib/api/client', () => ({
      api: {
        singboxRouterInspectRoute: inspectMock,
        singboxRouterInspectDNS: vi.fn().mockResolvedValue(dnsOk),
      },
    }));
    const { runTrace, traceInput } = await import('./traceStore');
    traceInput.set({ domain: 'netflix.com', port: 443, protocol: 'tcp' });

    await runTrace();
    expect(inspectMock).toHaveBeenCalledWith({ domain: 'netflix.com', port: 443, protocol: 'tcp' });
  });

  it('closeTrace() очищает result/error/input', async () => {
    const { openTrace, closeTrace, traceInput, traceResult, traceError } = await import('./traceStore');
    openTrace('netflix.com');
    traceResult.set({
      input: 'netflix.com', inputType: 'domain', matches: [],
      destination: 'direct', matchedRule: -1, final: 'direct',
    });
    traceError.set('something');

    closeTrace();
    expect(get(traceInput).domain).toBe('');
    expect(get(traceResult)).toBeNull();
    expect(get(traceError)).toBeNull();
  });
});
