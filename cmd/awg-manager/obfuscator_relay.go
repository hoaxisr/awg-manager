package main

import (
	"net"
	"sync/atomic"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// applyObfWatchdog применяет вердикт сторожа kernel-релея (спека §4.9).
// Ядро выключается в памяти сразу (tripped) — сбой записи настроек не должен
// оставить его выбираемым в этой жизни демона. Hash oops сохраняется только
// после записанного trip (или для чужого oops): при сбое записи следующий
// старт увидит тот же oops и сработает снова. Сработавшая метка boot_id
// WatchdogCheck уже снята — её при сбое записи повторить нечем, о чём и
// говорит журнал.
func applyObfWatchdog(reason, hash, lastOops string, trip, saveHash func(string) error,
	tripped *atomic.Bool, log *logging.ScopedLogger) {
	if reason != "" {
		tripped.Store(true)
		log.Error("obfuscator", "", "kernel-релей выключен сторожем: "+reason)
		if err := trip(reason); err != nil {
			log.Error("obfuscator", "", "выключатель kernel-релея не сохранён: "+err.Error()+
				" — ядро выключено до рестарта демона; срабатывание по метке загрузки после рестарта не повторится")
			return
		}
	}
	// Тот же hash не пишем: запись в /proc/mtdoops переживает рестарты, и
	// сохранение на каждом старте было бы записью на флеш впустую.
	if hash != "" && hash != lastOops {
		if err := saveHash(hash); err != nil {
			log.Warn("obfuscator", "", "hash oops не сохранён: "+err.Error())
		}
	}
}

// obfUseKernel — выбор бэкенда релея для диспетчера: ядро только для Phobos
// с IPv4-сервером, без выключателя «процесс», без срабатывания сторожа и при
// доступном модуле (отказ insmod запоминается в Available — §4.5).
func obfUseKernel(processForced, available func() bool, tripped *atomic.Bool) func(o *storage.Obfuscator, ip string) bool {
	return func(o *storage.Obfuscator, ip string) bool {
		p := net.ParseIP(ip)
		return o.Flavor == storage.ObfuscatorFlavorPhobos && p != nil && p.To4() != nil &&
			!tripped.Load() && !processForced() && available()
	}
}
