package vkauth

import (
	"strings"
	"sync"
	"sync/atomic"
)

type StreamCredentialsCache struct {
	creds         TurnCredentials
	mutex         sync.RWMutex
	errorCount    atomic.Int32
	lastErrorTime atomic.Int64
}

type Store struct {
	mu              sync.RWMutex
	caches          map[int]*StreamCredentialsCache
	streamsPerCache int
}

func NewStore(streamsPerCache int) *Store {
	if streamsPerCache <= 0 {
		streamsPerCache = DefaultStreamsPerCache
	}
	return &Store{
		caches:          make(map[int]*StreamCredentialsCache),
		streamsPerCache: streamsPerCache,
	}
}

// CacheID группирует потоки в блоки по streamsPerCache: потоки 1..streamsPerCache
// делят один кэш реквизитов, streamsPerCache+1.. - следующий. streamID 1-based
// (единый базис udprelay и tcpfwd); первый поток блока инициирует fetch к VK,
// остальные переиспользуют тёплый кэш.
func (s *Store) CacheID(streamID int) int {
	if streamID < 1 {
		return 0
	}
	return (streamID - 1) / s.streamsPerCache
}

func (s *Store) Get(streamID int) *StreamCredentialsCache {
	cacheID := s.CacheID(streamID)

	s.mu.RLock()
	cache, exists := s.caches[cacheID]
	s.mu.RUnlock()
	if exists {
		return cache
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if cache, exists = s.caches[cacheID]; exists {
		return cache
	}
	cache = &StreamCredentialsCache{}
	s.caches[cacheID] = cache
	return cache
}

func (c *StreamCredentialsCache) Invalidate() {
	c.mutex.Lock()
	c.creds = TurnCredentials{}
	c.mutex.Unlock()

	c.errorCount.Store(0)
	c.lastErrorTime.Store(0)
}

func IsAuthError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "401") ||
		strings.Contains(s, "Unauthorized") ||
		strings.Contains(s, "authentication") ||
		strings.Contains(s, "invalid credential") ||
		strings.Contains(s, "stale nonce")
}
