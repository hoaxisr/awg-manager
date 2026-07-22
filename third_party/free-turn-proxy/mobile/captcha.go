package mobile

import (
	"sync"
	"sync/atomic"
)

// CaptchaPresenter связывает ручное решение captcha с UI приложения.
//
//   - Show(url): открыть WebView на локальном прокси-адресе url, где пользователь
//     решает captcha. Метод блокирующим быть не обязан.
//   - Hide(): captcha решена или отменена - закрыть окно.
type CaptchaPresenter interface {
	Show(url string)
	Hide()
}

var (
	captchaMu        sync.RWMutex
	captchaPresenter CaptchaPresenter
)

// SetCaptchaPresenter регистрирует UI-презентер ручной captcha. Передайте nil,
// чтобы отключить ручной fallback - тогда при провале auto-captcha поток
// упадёт, как раньше. Вызывать один раз при старте приложения, до Start.
func SetCaptchaPresenter(p CaptchaPresenter) {
	captchaMu.Lock()
	captchaPresenter = p
	captchaMu.Unlock()
}

func currentCaptchaPresenter() CaptchaPresenter {
	captchaMu.RLock()
	defer captchaMu.RUnlock()
	return captchaPresenter
}

var manualCaptchaOnly atomic.Bool

// SetManualCaptcha включает/выключает режим "всегда решать captcha вручную".
// Применяется при следующем Start (или переподключении). Требует
// зарегистрированного презентера, иначе ручной путь недоступен.
func SetManualCaptcha(on bool) { manualCaptchaOnly.Store(on) }
