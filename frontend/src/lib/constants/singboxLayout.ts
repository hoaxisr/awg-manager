/** Sing-box surfaces: main tunnels tab, subscriptions tab, subscription detail members. */
export type SingboxLayoutMode = 'dense' | 'compact' | 'list';

/** How a tunnel surface renders — table on desktop list, cards otherwise. */
export type TunnelRenderMode = 'table' | 'list-card' | 'dense' | 'compact';

/** Layout choices of a tunnel surface: AWG `cards` plus the sing-box modes. */
export type TunnelSurfaceLayout = SingboxLayoutMode | 'cards';

export function resolveTunnelRenderMode(
	mobile: boolean,
	layout: TunnelSurfaceLayout,
): TunnelRenderMode {
	if (layout === 'list') return mobile ? 'list-card' : 'table';
	if (layout === 'dense' || layout === 'cards') return 'dense';
	return 'compact';
}

/**
 * Same breakpoint as the AWG tunnels tab (`isAwgMobile` on the home page).
 * Below this width: list mode uses compact card rows (dense header + actions).
 */
export const TUNNEL_MOBILE_LAYOUT_MAX_WIDTH_PX = 760;

export const SINGBOX_LAYOUT_STORAGE_KEY = 'singbox_layout_mode';

export function parseSingboxLayoutMode(value: string | null): SingboxLayoutMode | null {
	if (value === 'dense' || value === 'compact' || value === 'list') return value;
	// Legacy: previous two-mode toggle stored `grid` for the default card grid.
	if (value === 'grid') return 'compact';
	return null;
}

export function readTunnelMobileLayout(): boolean {
	if (typeof window === 'undefined') return false;
	return window.matchMedia(`(max-width: ${TUNNEL_MOBILE_LAYOUT_MAX_WIDTH_PX}px)`).matches;
}

export function subscribeTunnelMobileLayout(onChange: (mobile: boolean) => void): () => void {
	if (typeof window === 'undefined') return () => {};
	const media = window.matchMedia(`(max-width: ${TUNNEL_MOBILE_LAYOUT_MAX_WIDTH_PX}px)`);
	const sync = (event?: MediaQueryList | MediaQueryListEvent) => {
		onChange(event ? event.matches : media.matches);
	};
	sync(media);
	media.addEventListener('change', sync);
	return () => media.removeEventListener('change', sync);
}
