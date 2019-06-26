// Copyright 2016 Attic Labs, Inc. All rights reserved.
// Licensed under the Apache License, version 2.0:
// http://www.apache.org/licenses/LICENSE-2.0

package chunks

import (
	"context"
	"sync"

	"github.com/liquidata-inc/ld/dolt/go/store/constants"
	"github.com/liquidata-inc/ld/dolt/go/store/d"
	"github.com/liquidata-inc/ld/dolt/go/store/hash"
)

// MemoryStorage provides a "persistent" storage layer to back multiple
// MemoryStoreViews. A MemoryStorage instance holds the ground truth for the
// root and set of chunks that are visible to all MemoryStoreViews vended by
// NewView(), allowing them to implement the transaction-style semantics that
// ChunkStore requires.
type MemoryStorage struct {
	data     map[hash.Hash]Chunk
	rootHash hash.Hash
	mu       sync.RWMutex
}

// NewView vends a MemoryStoreView backed by this MemoryStorage. It's
// initialized with the currently "persisted" root.
func (ms *MemoryStorage) NewView() ChunkStore {
	return &MemoryStoreView{storage: ms, rootHash: ms.rootHash}
}

// Get retrieves the Chunk with the Hash h, returning EmptyChunk if it's not
// present.
func (ms *MemoryStorage) Get(ctx context.Context, h hash.Hash) (Chunk, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	if c, ok := ms.data[h]; ok {
		return c, nil
	}
	return EmptyChunk, nil
}

// Has returns true if the Chunk with the Hash h is present in ms.data, false
// if not.
func (ms *MemoryStorage) Has(ctx context.Context, r hash.Hash) bool {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	_, ok := ms.data[r]
	return ok
}

// Len returns the number of Chunks in ms.data.
func (ms *MemoryStorage) Len() int {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return len(ms.data)
}

// Root returns the currently "persisted" root hash of this in-memory store.
func (ms *MemoryStorage) Root(ctx context.Context) hash.Hash {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.rootHash
}

// Update checks the "persisted" root against last and, iff it matches,
// updates the root to current, adds all of novel to ms.data, and returns
// true. Otherwise returns false.
func (ms *MemoryStorage) Update(current, last hash.Hash, novel map[hash.Hash]Chunk) bool {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if last != ms.rootHash {
		return false
	}
	if ms.data == nil {
		ms.data = map[hash.Hash]Chunk{}
	}
	for h, c := range novel {
		ms.data[h] = c
	}
	ms.rootHash = current
	return true
}

// MemoryStoreView is an in-memory implementation of store.ChunkStore. Useful
// mainly for tests.
// The proper way to get one:
// storage := &MemoryStorage{}
// ms := storage.NewView()
type MemoryStoreView struct {
	pending  map[hash.Hash]Chunk
	rootHash hash.Hash
	mu       sync.RWMutex

	storage *MemoryStorage
}

func (ms *MemoryStoreView) Get(ctx context.Context, h hash.Hash) (Chunk, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	if c, ok := ms.pending[h]; ok {
		return c, nil
	}
	return ms.storage.Get(ctx, h)
}

func (ms *MemoryStoreView) GetMany(ctx context.Context, hashes hash.HashSet, foundChunks chan *Chunk) error {
	for h := range hashes {
		c, err := ms.Get(ctx, h)

		if err != nil {
			return err
		}

		if !c.IsEmpty() {
			foundChunks <- &c
		}
	}

	return nil
}

func (ms *MemoryStoreView) Has(ctx context.Context, h hash.Hash) bool {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	if _, ok := ms.pending[h]; ok {
		return true
	}
	return ms.storage.Has(ctx, h)
}

func (ms *MemoryStoreView) HasMany(ctx context.Context, hashes hash.HashSet) hash.HashSet {
	absent := hash.HashSet{}
	for h := range hashes {
		if !ms.Has(ctx, h) {
			absent.Insert(h)
		}
	}
	return absent
}

func (ms *MemoryStoreView) Version() string {
	return constants.NomsVersion
}

func (ms *MemoryStoreView) Put(ctx context.Context, c Chunk) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.pending == nil {
		ms.pending = map[hash.Hash]Chunk{}
	}
	ms.pending[c.Hash()] = c
}

func (ms *MemoryStoreView) Len() int {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return len(ms.pending) + ms.storage.Len()
}

func (ms *MemoryStoreView) Rebase(ctx context.Context) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.rootHash = ms.storage.Root(ctx)
}

func (ms *MemoryStoreView) Root(ctx context.Context) hash.Hash {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.rootHash
}

func (ms *MemoryStoreView) Commit(ctx context.Context, current, last hash.Hash) (bool, error) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if last != ms.rootHash {
		return false, nil
	}

	success := ms.storage.Update(current, last, ms.pending)
	if success {
		ms.pending = nil
	}
	ms.rootHash = ms.storage.Root(ctx)
	return success, nil
}

func (ms *MemoryStoreView) Stats() interface{} {
	return nil
}

func (ms *MemoryStoreView) StatsSummary() string {
	return "Unsupported"
}

func (ms *MemoryStoreView) Close() error {
	return nil
}

type memoryStoreFactory struct {
	stores map[string]*MemoryStorage
	mu     *sync.Mutex
}

func NewMemoryStoreFactory() *memoryStoreFactory {
	return &memoryStoreFactory{map[string]*MemoryStorage{}, &sync.Mutex{}}
}

func (f *memoryStoreFactory) CreateStoreFromCache(ctx context.Context, ns string) ChunkStore {
	return f.CreateStore(ctx, ns)
}

func (f *memoryStoreFactory) CreateStore(ctx context.Context, ns string) ChunkStore {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.stores == nil {
		d.Panic("Cannot use memoryStoreFactory after Shutter().")
	}
	if ms, present := f.stores[ns]; present {
		return ms.NewView()
	}
	f.stores[ns] = &MemoryStorage{}
	return f.stores[ns].NewView()
}

func (f *memoryStoreFactory) Shutter() {
	f.stores = nil
}