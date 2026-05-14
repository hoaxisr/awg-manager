/** Sing-box surfaces: main tunnels tab, subscriptions tab, subscription detail members. */
export type SingboxLayoutMode = 'grid' | 'list';

/**
 * Same breakpoint as the AWG tunnels tab (`isAwgMobile` on the home page).
 * Below this width, sing-box list tables do not fit; surfaces stay grid-only and the grid/list toggle is hidden.
 */
export const TUNNEL_MOBILE_LAYOUT_MAX_WIDTH_PX = 760;

export const SINGBOX_LAYOUT_STORAGE_KEY = 'singbox_layout_mode';

export function parseSingboxLayoutMode(value: string | null): SingboxLayoutMode | null {
	if (value === 'grid' || value === 'list') return value;
	return null;
}
