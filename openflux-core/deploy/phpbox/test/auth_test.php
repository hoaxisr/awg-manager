<?php
// PhpboxAuth: the captcha marker, the status field, and storing solved cookies. No network.
//   php deploy/phpbox/test/auth_test.php
require __DIR__ . '/../lib/util.php';
require __DIR__ . '/../lib/node.php';

$ok = true;
function check(string $name, bool $pass) { global $ok; printf("%-56s %s\n", $name, $pass ? 'OK' : 'FAIL'); $ok = $ok && $pass; }

$dir = sys_get_temp_dir() . '/phpbox-auth-' . getmypid();
@mkdir($dir, 0700, true);
$key = 'k1';
$log = new PhpboxLog("$dir/$key.log");

check('nothing pending at first', PhpboxAuth::pending($dir, $key) === null);
check('no cookies at first', !PhpboxAuth::haveCookies($dir, $key));

PhpboxAuth::need($dir, $key, 'https://passport.yandex.ru/auth?x=1');
$p = PhpboxAuth::pending($dir, $key);
check('pending shows the url to solve', is_array($p) && $p['url'] === 'https://passport.yandex.ru/auth?x=1' && $p['since'] > 0);

// A WebView CookieManager hands a one-line header: name=value; name=value
$r = PhpboxAuth::takeCookies($dir, $key, 'Session_id=abc.123; yandexuid=999; sessionid2=zzz', $log);
check('takeCookies ok for a header line', $r['ok'] === true);
check('pending cleared after cookies', PhpboxAuth::pending($dir, $key) === null);
check('haveCookies now true', PhpboxAuth::haveCookies($dir, $key));
$jar = file_get_contents(PhpboxAuth::jarFile($dir, $key));
check('jar is Netscape format with the domain', str_contains($jar, "# Netscape") && str_contains($jar, "\t.yandex.ru\t") === false && str_contains($jar, ".yandex.ru\tTRUE\t"));
check('jar carries each cookie', str_contains($jar, "\tSession_id\tabc.123") && str_contains($jar, "\tyandexuid\t999") && str_contains($jar, "\tsessionid2\tzzz"));
check('a running generation is told to recheck', is_file("$dir/$key.recheck"));

// curl can actually read it back as a cookie jar.
if (function_exists('curl_init')) {
    $ch = curl_init('http://127.0.0.1:1/');
    curl_setopt_array($ch, [CURLOPT_COOKIEFILE => PhpboxAuth::jarFile($dir, $key), CURLOPT_RETURNTRANSFER => true, CURLOPT_TIMEOUT => 1]);
    @curl_exec($ch);
    check('curl accepts the jar (no parse error)', curl_errno($ch) !== CURLE_READ_ERROR);
}

// JSON form with an explicit domain, and a Netscape file passed straight through.
$r2 = PhpboxAuth::takeCookies($dir, 'k2', '{"cookies":"a=1; b=2","domain":".example.com"}', $log);
check('JSON form stored', $r2['ok'] && str_contains(file_get_contents(PhpboxAuth::jarFile($dir, 'k2')), ".example.com\tTRUE\t"));
$r3 = PhpboxAuth::takeCookies($dir, 'k3', "# Netscape HTTP Cookie File\n.yandex.ru\tTRUE\t/\tTRUE\t0\tX\tY\n", $log);
check('a cookies.txt passes through', $r3['ok'] && str_contains(file_get_contents(PhpboxAuth::jarFile($dir, 'k3')), "\tX\tY"));
check('empty body refused', PhpboxAuth::takeCookies($dir, 'k4', '   ', $log)['ok'] === false);

// A status carries the captcha field.
PhpboxAuth::need($dir, 'k5', 'https://passport.yandex.ru/x');
$st = ['captcha' => PhpboxAuth::pending($dir, 'k5')];
check('status captcha field is set when pending', $st['captcha']['url'] === 'https://passport.yandex.ru/x');

PhpboxAuth::clear($dir, $key);
check('clear forgets cookies and marker', !PhpboxAuth::haveCookies($dir, $key) && PhpboxAuth::pending($dir, $key) === null);

array_map('unlink', glob("$dir/*") ?: []); @rmdir($dir);
echo $ok ? "AUTH TEST PASS\n" : "AUTH TEST FAIL\n";
exit($ok ? 0 : 1);
