// Чистая логика маршрутов (эндпоинтов) VLESS: общая для списка маршрутов и
// окна их настройки в карточке сервера.
import { m } from '$lib/i18n';
import type { XHTTPPreset, XHTTPTuning, XrayEndpointConfig } from '$lib/types';

export type EndpointScheme = 'cdn' | 'keendns_public' | 'keendns_cloud' | 'reality';

/** Хост из dest REALITY (`host:port`, допускает схему и путь). */
export function realityDestHost(d?: string): string {
	const v = (d || '').trim().replace(/^https?:\/\//, '').replace(/\/.*$/, '');
	const idx = v.lastIndexOf(':');
	return idx > 0 ? v.slice(0, idx) : v;
}

export function getEndpointScheme(ep: XrayEndpointConfig): Exclude<EndpointScheme, 'reality'> {
	if (ep.connection_way === 'external_https') return 'cdn';
	if (ep.origin_type === 'cloud_address') return 'keendns_cloud';
	return 'keendns_public';
}

/** Подпись и класс бейджа схемы — вычисляется при показе, следует за языком. */
export function getEndpointBadge(ep: XrayEndpointConfig): { label: string; cls: string } {
	if (ep.connection_way === 'external_https') {
		return { label: m.servers_xray_scheme_cdn(), cls: 'badge-info' };
	}
	if (ep.origin_type === 'cloud_address') {
		return { label: m.servers_xray_scheme_keendns_cloud(), cls: 'badge-purple' };
	}
	return { label: m.servers_xray_scheme_keendns_public(), cls: 'badge-success' };
}

export function createDefaultTuning(preset: XHTTPPreset = 'live_low_latency'): XHTTPTuning {
	return {
		preset,
		uplink_method: preset === 'cdn_get' ? 'GET' : 'POST',
		min_posts_interval_ms: preset === 'cdn_get' ? '5-10' : '',
		max_buffered_posts: preset === 'cdn_get' ? 200 : 1000,
		sc_max_each_post_bytes: preset === 'cdn_get' ? 65536 : 1000000,
		stream_up_server_secs: preset === 'live_low_latency' ? '' : '20-80',
		stream_path_suffix: '',
		enable_xmux: preset === 'balanced',
		x_padding_bytes: '100-500',
		x_padding_header: 'Referer',
		x_padding_placement: 'queryInHeader',
		x_padding_key: '_dc',
		x_padding_method: 'tokenish',
		x_padding_obfs_mode: true,
		session_placement: 'query',
		session_key: 'sid',
		seq_placement: 'query',
		seq_key: 'page',
		fp_chunk_bytes: 131072,
		fp_max_ways: 1,
	};
}
