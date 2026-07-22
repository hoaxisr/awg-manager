# AWG Manager patches (based on upstream v1.8.0)

Forked for router-friendly **auto-only** VK Smart Captcha.

## Changes vs samosvalishe/free-turn-proxy v1.8.0

1. **Auto orchestrator (WDTT-inspired, no WebView)**
   - 5 auto rounds per captcha challenge (`captchaAutoRounds`)
   - Each round rotates browser persona starting from `-browser`, then the other two
   - Fresh TLS client + `browser_fp` per round
   - **Global captcha mutex** on whole process (not per VK link): `multi(4)` had 4 parallel captcha slots

2. **Stronger Go solver**
   - 4 internal attempts per round (was 2)
   - BOT and `status=ERROR` retry with new identity instead of blind backoff
   - Checkbox BOT/ERROR → fallback to slider when settings available

3. **Captcha solver (awg.3–awg.4)**
   - Checkbox `show_type=slider` / `status=BOT` → slider without "show type mismatch" dead-end
   - `ERROR_LIMIT` → backoff 2–5s and retry within round (not instant fail)
   - `getContent status=ERROR` → session burned: fail fast, request fresh VK challenge (awg.4)
   - **Host captcha lock** (`/tmp/freeturn-vk-captcha.lock`): one captcha slot across freeturn **processes** on the same router (awg.4)

4. **No fatal on cold start**
   - Exhausted auto captcha with 0 connected streams → 60s lockout + `CAPTCHA_WAIT_REQUIRED` (retry), not process kill

4. **Manual captcha disabled by default**
   - No `:8765` HTTP server unless `-captcha-manual-fallback` or `-manual-captcha`
   - `-manual-captcha` = manual-only (legacy)
   - `-captcha-manual-fallback` = auto rounds then browser on localhost (not recommended on Keenetic)

## Build

From awg-manager repo root:

```bash
./scripts/build-freeturn-client.sh        # all arches
./scripts/build-freeturn-client.sh arm64  # router aarch64
```

Output: `prebuilt/freeturn/client-linux-*` and `server-linux-*`

Version baked in: `1.8.0-awg.4` (`main.version` ldflag)
