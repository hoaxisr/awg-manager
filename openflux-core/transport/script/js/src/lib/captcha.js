// Shared Yandex SmartCaptcha handling: redirect-chain walk to
// showcaptchafast, PoW solve, fingerprint build/encode, and the POST that
// answers the challenge. Literal port of transport/yandex/captcha.go's
// solveCaptcha, previously duplicated verbatim across yandex.js, vyandex.js
// and boards.js - this is the single copy, pulled in via require().
//
// Depends only on host globals every script already has (http, crypto,
// base64, gzip, text) plus a caller-supplied User-Agent string - no
// transport-specific state, so it has nothing else to import.

var SSR_DATA_RE = /window\.__SSR_DATA__\s*=\s*JSON\.parse\(atob\("([^"]+)"\)\)/;
var FORM_ACTION_RE = /<form[^>]*id="tmgrdfrend-form"[^>]*action="([^"]+)"/;

function browserHeaders(ua) {
  return {
    "User-Agent": ua,
    Accept: "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
    "Accept-Language": "en-US,en;q=0.9",
    "Sec-GPC": "1",
    "Upgrade-Insecure-Requests": "1",
    "Sec-Fetch-Dest": "document",
    "Sec-Fetch-Mode": "navigate",
    "Sec-Fetch-Site": "none",
    "Sec-Fetch-User": "?1",
    Pragma: "no-cache",
    "Cache-Control": "no-cache",
  };
}

function buildCaptchaFingerprint(ua, nonceHex) {
  return {
    b6: 8, b7: 8, b9: ["en-US", "en"],
    c2: "", c4: "MacIntel", c5: [], c9: ua,
    f4: 1080, f5: 1920, f6: 24, f7: 1080, f8: true,
    f9: [1920, 1080], g1: 1920,
    g2: "Europe/Moscow", g3: -180,
    j5: true,
    m2: { mTP: 0, tE: false, tS: false },
    n6: false,
    o2: 0, o3: "srgb", o4: 0, o5: "en-US",
    o8: null, o9: null,
    p1: null, p2: 0, p3: null, p4: null,
    p5: null, p6: null, p8: [], p9: "111111111",
    j6: 48000,
    a1: "",
    a2: { w: false, d: "" },
    a3: {
      acos: 1.4444399284962483, asin: 0.12349655394506357,
      atan: 0.4636476090008061, cos: -0.8390715290095377,
      exp: 2.718281828459045, log1p: 2.3978952727983707,
      sin: -0.9917788534431158, tan: -0.23206847684369653,
    },
    a4: { minDelta: 0.1, maxDelta: 1.2 },
    a5: null,
    k4: [],
    j1: { vn: "WebKit", vr: "WebKit WebGL", vU: "", r: "Mozilla", rU: "", sLV: "WebGL GLSL ES 1.0 (1.0)" },
    j2: { cA: [], p: [], sP: [], e: [], eP: [] },
    m10: nonceHex,
    version: "1.5.0",
  };
}

function encodeCaptchaFingerprint(fp) {
  return "~" + base64.encode(gzip.compress(text.encode(JSON.stringify(fp)))) + "~";
}

function parseCaptchaHTML(html) {
  var m = SSR_DATA_RE.exec(html);
  if (!m) throw new Error("captcha: __SSR_DATA__ not found");
  var ssr = JSON.parse(text.decode(base64.decode(m[1])));
  var m2 = FORM_ACTION_RE.exec(html);
  if (!m2) throw new Error("captcha: form action not found");
  var formAction = m2[1].split("&amp;").join("&");
  if (formAction.indexOf("/") === 0) formAction = "https://docs.yandex.ru" + formAction;
  return { ssr: ssr, formAction: formAction };
}

async function solveCaptcha(ua, originalURL) {
  var captchaURL = "";
  var currentURL = originalURL;
  for (var i = 0; i < 10; i++) {
    var res = await http.fetch({ url: currentURL, headers: browserHeaders(ua), redirect: "manual" });
    if (res.status === 200) return; // no captcha needed after all
    if (res.status < 300 || res.status >= 400) throw new Error("captcha unexpected status " + res.status);
    var loc = res.headers["Location"];
    if (!loc) throw new Error("captcha: redirect without Location");
    if (loc.indexOf("showcaptchafast") !== -1) { captchaURL = loc; break; }
    currentURL = loc;
  }
  if (!captchaURL) throw new Error("captcha: showcaptchafast not found in redirect chain");

  var page = await http.fetch({ url: captchaURL, headers: browserHeaders(ua), redirect: "manual" });
  if (page.status !== 200) throw new Error("captcha showcaptcha status " + page.status);

  var parsed = parseCaptchaHTML(page.body);
  var solved = crypto.solvePow(parsed.ssr.pow.prefix, parsed.ssr.pow.complexity);
  var fpEncoded = encodeCaptchaFingerprint(buildCaptchaFingerprint(ua, solved.nonceHex));

  var form = "version=1.5.0" +
    "&uniquekey=" + encodeURIComponent(parsed.ssr.uniqueKey) +
    "&chstate=ok" +
    "&fingerprint=" + encodeURIComponent(fpEncoded);
  var postHeaders = browserHeaders(ua);
  postHeaders["Content-Type"] = "application/x-www-form-urlencoded";
  postHeaders["Origin"] = "https://docs.yandex.ru";
  postHeaders["Referer"] = captchaURL;

  var postRes = await http.fetch({ url: parsed.formAction, method: "POST", headers: postHeaders, body: form, redirect: "manual" });
  if (postRes.status < 300 || postRes.status >= 400) throw new Error("captcha POST unexpected status " + postRes.status);
}

module.exports = {
  browserHeaders: browserHeaders,
  buildCaptchaFingerprint: buildCaptchaFingerprint,
  encodeCaptchaFingerprint: encodeCaptchaFingerprint,
  parseCaptchaHTML: parseCaptchaHTML,
  solveCaptcha: solveCaptcha,
};
