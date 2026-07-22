// Package provider определяет абстракцию источника TURN-реквизитов.
//
// Реализации (internal/provider/vk, ...) поставляют
// короткоживущие user/pass/addr для TURN-allocate. Pipeline (proxy/udprelay,
// proxy/tcpfwd) работает только через этот интерфейс - никаких VK-specific
// типов в общем коде.
//
// Sentinel-ошибки описывают provider-нейтральные состояния, которые pipeline
// обрабатывает однотипно: ErrBackoffActive - провайдер просит подождать,
// ErrFatalNoStreams - фатально, дальше пробовать бессмысленно.
package provider

import (
	"context"
	"errors"
	"time"
)

// Credentials - выданные провайдером TURN-реквизиты для одного стрима.
// ServerAddrs - кандидаты host:port в порядке предпочтения (первый -
// предпочтительный для этого streamID). Pipeline пробует их по очереди при
// allocate: если первый не проходит (DPI-дроп/RST), берёт следующий.
type Credentials struct {
	User        string
	Pass        string
	ServerAddrs []string  // host:port, primary-first
	ExpiresAt   time.Time // 0 если провайдер не сообщает дедлайн
}

// Provider - источник TURN-реквизитов для клиентского pipeline.
// Реализации обязаны быть thread-safe: pipeline вызывает GetCredentials и
// HandleAuthError из N разных горутин одновременно (по числу стримов).
type Provider interface {
	// GetCredentials блокирующе возвращает реквизиты для streamID.
	// Реализация отвечает за throttling, кеширование, ретраи к upstream-API.
	GetCredentials(ctx context.Context, streamID int) (Credentials, error)

	// IsAuthError - провайдер сам решает, какие ошибки от TURN-allocate
	// (401/403/stale-nonce/etc.) считать auth-related и приводить к
	// инвалидации кеша через HandleAuthError.
	IsAuthError(err error) bool

	// HandleAuthError увеличивает счётчик auth-ошибок для streamID.
	// Возвращает true, если порог достигнут и кеш для streamID инвалидирован.
	HandleAuthError(streamID int) bool

	// ResetErrors обнуляет счётчик ошибок (вызывать после успешного allocate).
	ResetErrors(streamID int)

	// BackoffUntilUnix - unix-секунда, до которой провайдер просит pipeline
	// не звать GetCredentials. 0 - нет активного бэк-оффа.
	// Используется только при возврате ошибки, обёрнутой ErrBackoffActive.
	BackoffUntilUnix() int64

	// Name - короткое имя провайдера для логов ("vk", ...).
	Name() string
}

// Sentinel-ошибки, которые провайдер оборачивает (через errors.Join или
// fmt.Errorf("%w: ...", ErrXxx, inner)) для сигнализации pipeline.
var (
	// ErrBackoffActive - провайдер просит подождать. Pipeline вызывает
	// BackoffUntilUnix() для получения точного дедлайна; если 0 - sleep по
	// дефолтному backoff (60s).
	ErrBackoffActive = errors.New("provider backoff active")

	// ErrFatalNoStreams - провайдер не может выдать креды и ни один стрим
	// ещё не поднят. Pipeline возвращает фатальную ошибку наружу
	// (хост-процесс делает os.Exit без вмешательства pipeline).
	ErrFatalNoStreams = errors.New("provider fatal: no streams alive")
)
