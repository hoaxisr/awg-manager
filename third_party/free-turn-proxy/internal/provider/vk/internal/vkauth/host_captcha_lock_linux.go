//go:build linux

package vkauth

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
)

const defaultHostCaptchaLockPath = "/tmp/freeturn-vk-captcha.lock"

func hostCaptchaLockPath() string {
	if p := os.Getenv("FREETURN_CAPTCHA_LOCK"); p != "" {
		return p
	}
	return defaultHostCaptchaLockPath
}

// acquireHostCaptchaLock serializes VK captcha across freeturn processes on the
// same host (same public IP). Without this, two awg-manager freeturn clients
// bypass in-process globalCaptchaMu and trip VK ERROR_LIMIT together.
func acquireHostCaptchaLock(ctx context.Context, streamID int, log logx.Logger) (func(), error) {
	path := hostCaptchaLockPath()
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0755)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		log.Warnf("[STREAM %d] [Captcha] host lock unavailable (%s): %v", streamID, path, err)
		return func() {}, nil
	}

	waiting := false
	for {
		if flockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); flockErr == nil {
			if waiting {
				log.Infof("[STREAM %d] [Captcha] Acquired host captcha slot", streamID)
			}
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !waiting {
			log.Infof("[STREAM %d] [Captcha] Waiting for host captcha slot (another freeturn process on this router)", streamID)
			waiting = true
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}
