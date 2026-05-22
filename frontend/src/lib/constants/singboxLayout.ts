/** Sing-box surfaces: main tunnels tab, subscriptions tab, subscription detail members. */
export type SingboxLayoutMode = 'dense' | 'compact' | 'list';

/**
 * Same breakpoint as the AWG tunnels tab (`isAwgMobile` on the home page).
 * Below this width, sing-box list tables do not fit; surfaces stay compact grid and the view toggle is hidden.
 */
export const TUNNEL_MOBILE_LAYOUT_MAX_WIDTH_PX = 760;

export const SINGBOX_LAYOUT_STORAGE_KEY = 'singbox_layout_mode';

export function parseSingboxLayoutMode(value: string | null): SingboxLayoutMode | null {
	if (value === 'dense' || value === 'compact' || value === 'list') return value;
	// Legacy: previous two-mode toggle stored `grid` for the default card grid.
	if (value === 'grid') return 'compact';
	return null;
}
