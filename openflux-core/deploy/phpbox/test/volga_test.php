<?php
// VolgaAuth pure parsers + data codec, and the PoW bit-count. No network.
//   php deploy/phpbox/test/volga_test.php
require __DIR__ . '/../lib/volga.php';

$ok = true;
function check(string $name, bool $pass, string $why = '') { global $ok; printf("%-56s %s\n", $name, $pass ? 'OK' : "FAIL  $why"); $ok = $ok && $pass; }

// client-config extraction
$html = '<html><head><script id="client-config" type="application/json">{"officeActionData":{"action_url":"https://disk.yandex.ru/auth/initial","access_token":"TOK","access_token_ttl":3600,"resource_url":"R"},"editorParams":{"idDoc":"vyd:xyz","action":"edit"}}</script></head><body>x</body></html>';
$cfg = VolgaAuth::clientConfig($html);
check('client-config parsed', is_array($cfg) && $cfg['officeActionData']['access_token'] === 'TOK' && $cfg['editorParams']['idDoc'] === 'vyd:xyz');
check('client-config absent -> null', VolgaAuth::clientConfig('<html>no config</html>') === null);

// auth/initial Location parse
$loc = 'https://volga.yandex.ru/document/?token=TKN&request-path=main/abc123&json=' . rawurlencode(json_encode([
    'sessionId' => 'S1', 'userId' => 42, 'xiva' => ['sign' => 'SG', 'ts' => '1700000000', 'user' => '42'],
]));
$p = VolgaAuth::parseLocation($loc);
check('Location token/request-path', $p['token'] === 'TKN' && $p['request_path'] === 'main/abc123');
check('Location xiva sign/ts/user', $p['sign'] === 'SG' && $p['ts'] === '1700000000' && $p['user'] === '42');
check('Location userId/sessionId', $p['user_id'] === 42 && $p['session_id'] === 'S1');

// ttl formatting
check('ttl int', VolgaAuth::ttl(3600) === '3600');
check('ttl float', VolgaAuth::ttl(3600.0) === '3600');
check('ttl string', VolgaAuth::ttl('7200') === '7200');
check('ttl null', VolgaAuth::ttl(null) === '0');

// data codec round-trip, same framing as the Go side ([uint16 len][bytes], base64)
$packets = ['', 'A', str_repeat("\x00\xff", 1000), 'a mux frame here'];
$blob = VolgaAuth::packetsToBlob($packets);
check('blob is base64', base64_decode($blob, true) !== false);
$back = VolgaAuth::blobToPackets($blob);
check('codec round-trips every packet in order', $back === $packets, var_export($back, true));
// a known blob the Go side would make: two packets "hi","yo"
$known = base64_encode(pack('n', 2) . 'hi' . pack('n', 2) . 'yo');
check('matches the Go framing (2-byte big-endian length)', VolgaAuth::blobToPackets($known) === ['hi', 'yo']);
check('a truncated blob yields the whole packets only', VolgaAuth::blobToPackets(base64_encode(pack('n', 2) . 'hi' . pack('n', 5) . 'ab')) === ['hi']);
check('garbage base64 -> no packets', VolgaAuth::blobToPackets('!!!notbase64!!!') === []);

// PoW bit counting
check('leadingZeroBits 0x00ff.. = 8', VolgaPoW::leadingZeroBits("\x00\xff") === 8);
check('leadingZeroBits 0x0000.. = 16+', VolgaPoW::leadingZeroBits("\x00\x00\x80") === 16);
check('leadingZeroBits 0x80 = 0', VolgaPoW::leadingZeroBits("\x80") === 0);
check('leadingZeroBits 0x20 = 2', VolgaPoW::leadingZeroBits("\x20") === 2);
$nonce = VolgaPoW::solvePoW(bin2hex('prefix'), 12);
check('solvePoW finds a nonce meeting the difficulty', $nonce !== '' && VolgaPoW::leadingZeroBits(hash('sha256', 'prefix' . hex2bin($nonce), true)) >= 12);

echo $ok ? "VOLGA TEST PASS\n" : "VOLGA TEST FAIL\n";
exit($ok ? 0 : 1);
