// Package iproute2 проверяет, что /opt/sbin/ip — полноценный iproute2 из
// пакета ip-full, а не ссылка на busybox: busybox-овый ip не знает части
// команд демона и отказывает с кодом выхода без внятной причины в журнале.
package iproute2

import (
	"context"
	"errors"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// Path — бинарь, который вызывает демон.
const Path = "/opt/sbin/ip"

// Check возвращает ошибку, если Path не отвечает как iproute2.
func Check(ctx context.Context) error {
	res, err := exec.Run(ctx, Path, "-V")
	return classify(res.Stdout+res.Stderr, err)
}

func classify(out string, err error) error {
	switch {
	case strings.Contains(out, "BusyBox"):
		return errors.New(Path + " — ссылка на busybox, а не iproute2; выполните: opkg install --force-reinstall ip-full")
	case err != nil:
		return errors.New(Path + " не запускается (" + err.Error() + "); выполните: opkg install --force-reinstall ip-full")
	case !strings.Contains(out, "iproute2"):
		return errors.New(Path + " не похож на iproute2: " + strings.TrimSpace(out))
	}
	return nil
}
