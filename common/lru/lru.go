// Copyright 2022 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package lru

import "sync"

// Cache is a LRU cache.
// This type is safe for concurrent use.
type Cache[K comparable, V any] struct {
	cache BasicLRU[K, V]
	mu    sync.Mutex
}

// NewCache creates an LRU cache.
func NewCache[K comparable, V any](capacity int) *Cache[K, V] {
	return &Cache[K, V]{cache: NewBasicLRU[K, V](capacity)}
}

// Add adds a value to the cache. Returns true if an item was evicted to store the new item.
func (c *Cache[K, V]) Add(key K, value V) (evicted bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.cache.Add(key, value)
}

// Contains reports whether the given key exists in the cache.
func (c *Cache[K, V]) Contains(key K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.cache.Contains(key)
}

// Get retrieves a value from the cache. This marks the key as recently used.
func (c *Cache[K, V]) Get(key K) (value V, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.cache.Get(key)
}

// Len returns the current number of items in the cache.
func (c *Cache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.cache.Len()
}

// Peek retrieves a value from the cache, but does not mark the key as recently used.
func (c *Cache[K, V]) Peek(key K) (value V, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.cache.Peek(key)
}

// Purge empties the cache.
func (c *Cache[K, V]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cache.Purge()
}

// Remove drops an item from the cache. Returns true if the key was present in cache.
func (c *Cache[K, V]) Remove(key K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.cache.Remove(key)
}

// Keys returns all keys of items currently in the LRU.
func (c *Cache[K, V]) Keys() []K {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.cache.Keys()
}

type GroupCache[K1, K2 comparable, V any] struct {
	cache GroupLRU[K1, K2, V]
	mu    sync.Mutex
}

func NewGroupCache[K1, K2 comparable, V any](capacity int) *GroupCache[K1, K2, V] {
	return &GroupCache[K1, K2, V]{cache: NewGroupLRU[K1, K2, V](capacity)}
}

func (c *GroupCache[K1, K2, V]) Add(key1 K1, key2 K2, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cache.Add(key1, key2, value)
}

func (c *GroupCache[K1, K2, V]) Get(key1 K1, key2 K2) (value V, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.cache.Get(key1, key2)
}

func (c *GroupCache[K1, K2, V]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cache.Purge()
}

func (c *GroupCache[K1, K2, V]) Remove(key1 K1, key2 K2) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cache.Remove(key1, key2)
}

func (c *GroupCache[K1, K2, V]) RemoveGroup(key1 K1) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cache.RemoveGroup(key1)
}
