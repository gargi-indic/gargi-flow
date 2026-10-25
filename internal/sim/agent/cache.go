package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
)

// CacheKey represents a unique key for an agent cache entry.
type CacheKey struct {
	InputHash   string
	Model       string
	Replication uint64
}

// GlobalCache is a global thread-safe response cache for agents.
// Reruns of identical experiments will hit this cache.
var GlobalCache = NewCache()

// Cache provides a thread-safe deterministic response cache.
type Cache struct {
	mu      sync.RWMutex
	entries map[CacheKey][]byte
}

// NewCache creates a new deterministic response cache.
func NewCache() *Cache {
	return &Cache{
		entries: make(map[CacheKey][]byte),
	}
}

// GenerateInputHash generates a SHA-256 hash of a canonically formatted JSON payload.
func GenerateInputHash(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:]), nil
}

// Get retrieves a response from the cache.
func (c *Cache) Get(key CacheKey, out any) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	b, ok := c.entries[key]
	if !ok {
		return false
	}
	_ = json.Unmarshal(b, out)
	return true
}

// Set stores a response in the cache.
func (c *Cache) Set(key CacheKey, val any) {
	b, _ := json.Marshal(val)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = b
}

// Clear removes all entries from the cache.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[CacheKey][]byte)
}
