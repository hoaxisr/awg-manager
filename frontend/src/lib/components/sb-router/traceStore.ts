/**
 * State + URL sync для F4b Trace Screen.
 *
 * URL contract:
 *   ?trace=1            → traceOpen=true (screen visible)
 *   ?trace=1&q=domain   → ditto + traceInput.domain pre-filled (auto-run on init)
 *   no ?trace           → traceOpen=false (fallback к FlowGraph+RulesPanel)
 *
 * API call идёт через api.singboxRouterInspectRoute — backend уже готов.
 */
import { writable, type Readable, type Writable } from 'svelte/store';
import { api } from '$lib/api/client';
import type {
  SingboxRouterInspectResult,
  SingboxRouterInspectDNSResult,
} from '$lib/types';

export interface TraceInput {
  domain: string;
  port?: number;
  protocol?: string;
  /** DNS query type for the Step-1 DNS branch (e.g. "A"/"AAAA"); empty = any. */
  queryType?: string;
  /** Optional source/client IP feeding source_ip_cidr matching (both branches). */
  sourceIP?: string;
}

function readURL(): { open: boolean; domain: string } {
  if (typeof window === 'undefined') return { open: false, domain: '' };
  const sp = new URL(window.location.href).searchParams;
  return {
    open: sp.get('trace') === '1',
    domain: sp.get('q') ?? '',
  };
}

function updateURL(open: boolean, domain: string): void {
  if (typeof window === 'undefined') return;
  try {
    const url = new URL(window.location.href);
    if (open) {
      url.searchParams.set('trace', '1');
      if (domain) {
        url.searchParams.set('q', domain);
      } else {
        url.searchParams.delete('q');
      }
    } else {
      url.searchParams.delete('trace');
      url.searchParams.delete('q');
    }
    window.history.replaceState({}, '', url.toString());
  } catch {
    /* non-browser env or restricted history — ignore */
  }
}

const initial = readURL();
const openStore = writable<boolean>(initial.open);

export const traceOpen: Readable<boolean> = { subscribe: openStore.subscribe };
export const traceInput: Writable<TraceInput> = writable<TraceInput>({ domain: initial.domain });
export const traceResult: Writable<SingboxRouterInspectResult | null> = writable<SingboxRouterInspectResult | null>(null);
export const traceLoading: Writable<boolean> = writable<boolean>(false);
export const traceError: Writable<string | null> = writable<string | null>(null);

// Step 1 — DNS-branch result (which dns.rule matches → which server → fakeip/real/local).
export const dnsResult: Writable<SingboxRouterInspectDNSResult | null> = writable<SingboxRouterInspectDNSResult | null>(null);
export const dnsLoading: Writable<boolean> = writable<boolean>(false);
export const dnsError: Writable<string | null> = writable<string | null>(null);

export function openTrace(domain?: string): void {
  if (domain !== undefined) {
    traceInput.update((cur) => ({ ...cur, domain }));
  }
  openStore.set(true);
  let currentDomain = '';
  traceInput.subscribe((v) => { currentDomain = v.domain; })();
  updateURL(true, currentDomain);
}

export function closeTrace(): void {
  openStore.set(false);
  traceInput.set({ domain: '' });
  traceResult.set(null);
  traceError.set(null);
  dnsResult.set(null);
  dnsError.set(null);
  updateURL(false, '');
}

/**
 * Runs BOTH inspector branches for one domain: Step 1 (DNS-решение) via
 * inspectDNS and Step 2 (route) via inspectRoute. The mockup shows both
 * steps stacked for a single «Проверить», so they fire together and each
 * tracks its own loading/error state independently.
 */
export async function runTrace(): Promise<void> {
  let req: TraceInput = { domain: '' };
  traceInput.subscribe((v) => { req = v; })();
  const domain = req.domain.trim();
  if (!domain) return;

  traceLoading.set(true);
  traceError.set(null);
  dnsLoading.set(true);
  dnsError.set(null);

  const routeP = api
    .singboxRouterInspectRoute({
      domain,
      ...(req.port != null ? { port: req.port } : {}),
      ...(req.protocol ? { protocol: req.protocol } : {}),
    })
    .then((result) => { traceResult.set(result); })
    .catch((e) => { traceError.set(e instanceof Error ? e.message : String(e)); })
    .finally(() => { traceLoading.set(false); });

  const dnsP = api
    .singboxRouterInspectDNS({
      domain,
      ...(req.queryType ? { queryType: req.queryType } : {}),
      ...(req.sourceIP ? { sourceIP: req.sourceIP } : {}),
    })
    .then((result) => { dnsResult.set(result); })
    .catch((e) => { dnsError.set(e instanceof Error ? e.message : String(e)); })
    .finally(() => { dnsLoading.set(false); });

  await Promise.all([routeP, dnsP]);
  updateURL(true, domain);
}
