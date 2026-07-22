// VK Smart Captcha PoW needs crypto.subtle.digest, which browsers block outside
// secure contexts (HTTPS or localhost). awg-manager is often opened via
// http://192.168.x.x — inject this shim into proxied captcha HTML.
(function () {
	'use strict';
	var c = globalThis.crypto;
	if (!c) {
		c = {};
		globalThis.crypto = c;
	}
	if (c.subtle && typeof c.subtle.digest === 'function') return;

	function rotr(n, x) {
		return (x >>> n) | (x << (32 - n));
	}

	function sha256(msg) {
		var K = [
			0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
			0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
			0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
			0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
			0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
			0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
			0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
			0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2
		];
		var H = [
			0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19
		];
		var i;
		var bitLen = msg.length * 8;
		var withLen = new Uint8Array(((msg.length + 9 + 63) >> 6) << 6);
		withLen.set(msg);
		withLen[msg.length] = 0x80;
		var view = new DataView(withLen.buffer);
		view.setUint32(withLen.length - 4, bitLen, false);

		for (var off = 0; off < withLen.length; off += 64) {
			var w = new Uint32Array(64);
			for (i = 0; i < 16; i++) w[i] = view.getUint32(off + i * 4, false);
			for (i = 16; i < 64; i++) {
				var s0 = rotr(7, w[i - 15]) ^ rotr(18, w[i - 15]) ^ (w[i - 15] >>> 3);
				var s1 = rotr(17, w[i - 2]) ^ rotr(19, w[i - 2]) ^ (w[i - 2] >>> 10);
				w[i] = (w[i - 16] + s0 + w[i - 7] + s1) >>> 0;
			}
			var a = H[0],
				b = H[1],
				c2 = H[2],
				d = H[3],
				e = H[4],
				f = H[5],
				g = H[6],
				h = H[7];
			for (i = 0; i < 64; i++) {
				var S1 = rotr(6, e) ^ rotr(11, e) ^ rotr(25, e);
				var ch = (e & f) ^ (~e & g);
				var t1 = (h + S1 + ch + K[i] + w[i]) >>> 0;
				var S0 = rotr(2, a) ^ rotr(13, a) ^ rotr(22, a);
				var maj = (a & b) ^ (a & c2) ^ (b & c2);
				var t2 = (S0 + maj) >>> 0;
				h = g;
				g = f;
				f = e;
				e = (d + t1) >>> 0;
				d = c2;
				c2 = b;
				b = a;
				a = (t1 + t2) >>> 0;
			}
			H[0] = (H[0] + a) >>> 0;
			H[1] = (H[1] + b) >>> 0;
			H[2] = (H[2] + c2) >>> 0;
			H[3] = (H[3] + d) >>> 0;
			H[4] = (H[4] + e) >>> 0;
			H[5] = (H[5] + f) >>> 0;
			H[6] = (H[6] + g) >>> 0;
			H[7] = (H[7] + h) >>> 0;
		}
		var out = new Uint8Array(32);
		var outView = new DataView(out.buffer);
		for (i = 0; i < 8; i++) outView.setUint32(i * 4, H[i], false);
		return out;
	}

	c.subtle = {
		digest: function (algorithm, data) {
			var name = typeof algorithm === 'string' ? algorithm : algorithm && algorithm.name;
			if (name !== 'SHA-256') {
				return Promise.reject(new Error('Only SHA-256 is supported by captcha shim'));
			}
			var u8 =
				data instanceof ArrayBuffer
					? new Uint8Array(data)
					: new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
			return Promise.resolve(sha256(u8).buffer);
		}
	};
})();
