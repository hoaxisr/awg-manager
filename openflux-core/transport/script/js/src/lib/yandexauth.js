// The Yandex sign-in every Yandex transport declares in info().auth: one
// account for yandex, vyandex, boards and telemost. The apps build their
// Yandex account from it (passport sign-in in the built-in browser, done once
// back on Disk) and hand a transport only the cookies listed here.
//
// need is per transport (a board is joined as a guest; a Telemost conference
// needs the sign-in only to be created), so each one passes its own.
function auth(need, cookies) {
  var a = {
    account: "yandex",
    label: "Яндекс",
    need: need,
    signInUrl: "https://passport.yandex.ru/auth?retpath=https%3A%2F%2Fdisk.yandex.ru%2Fclient%2Fdisk",
    homeUrl: "https://disk.yandex.ru/client",
    // Yandex sets Session_id before the rest of the session: the sign-in
    // counts once the page is back on Disk.
    waitForHome: true,
    cookieUrls: ["https://passport.yandex.ru/", "https://yandex.ru/", "https://disk.yandex.ru/", "https://docs.yandex.ru/"],
    loginCookie: "Session_id",
    loginNameCookie: "yandex_login",
  };
  if (cookies && cookies.length) a.cookies = cookies;
  return a;
}

module.exports = { auth: auth };
