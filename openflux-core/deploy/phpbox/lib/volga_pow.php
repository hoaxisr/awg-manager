<?php
// The "showcaptchafast" proof-of-work captcha the Volga authorization can hit. Yandex serves a small PoW challenge
// (a prefix and a difficulty); the client finds a nonce whose SHA-256 has enough leading zero bits, posts it with a
// browser fingerprint, and gets a cookie that lets the request through. A headless node can do this itself.
//
// This mirrors transport/yandex/captcha.go. It is NOT the SmartCaptcha (showcaptcha) — that needs a human, and the
// node hands it to the user instead (PhpboxAuth::need).
//
// NOTE: the fingerprint Yandex accepts changes, and a datacenter IP may be refused the PoW path altogether and sent
// straight to SmartCaptcha. solve() returns false when it cannot pass; the caller then asks the user to solve it.

require_once __DIR__ . '/util.php';

final class VolgaPoW
{
    /** Finds a 16-byte nonce whose SHA-256(nonce) has >= complexity leading zero bits; returns it hex-encoded. */
    public static function solvePoW(string $prefixHex, int $complexity): string
    {
        $prefix = hex2bin($prefixHex) ?: '';
        for ($attempt = 0; $attempt < 5_000_000; $attempt++) {
            $nonce = pack('P', (int)(microtime(true) * 1e6)) . random_bytes(8);
            $h = hash('sha256', $prefix . $nonce, true);
            if (self::leadingZeroBits($h) >= $complexity) {
                return bin2hex($nonce);
            }
        }
        return '';
    }

    public static function leadingZeroBits(string $bin): int
    {
        $bits = 0;
        $len = strlen($bin);
        for ($i = 0; $i < $len; $i++) {
            $b = ord($bin[$i]);
            if ($b === 0) { $bits += 8; continue; }
            for ($m = 7; $m >= 0; $m--) {
                if (($b >> $m) & 1) { return $bits; }
                $bits++;
            }
        }
        return $bits;
    }

    /**
     * Solves the showcaptchafast challenge reached at $startURL, writing the pass cookie into $cookieFile.
     * Returns true on success. Not yet wired to the live challenge fetch/submit (the fingerprint Yandex accepts
     * needs to be matched against the live service first), so today it reports that a human is needed.
     */
    public static function solve(string $startURL, string $cookieFile, string $userAgent, ?callable $say = null): bool
    {
        if ($say) { $say('showcaptchafast (PoW) seen; the automated solver is not wired up yet, asking the user'); }
        return false;
    }
}
