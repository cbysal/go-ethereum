package state

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/lru"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
)

const (
	accountCacheSize = 1024 * 1024
	storageCacheSize = 1024 * 1024
)

type SlimReader struct {
	db            ethdb.ReversibleKeyValueStore
	height        uint64
	accountCache  *lru.Cache[common.Address, *types.StateAccount]
	storageCache  *lru.GroupCache[common.Address, common.Hash, common.Hash]
	codeCache     *lru.SizeConstrainedCache[common.Hash, []byte]
	codeSizeCache *lru.Cache[common.Hash, int]
}

func NewSlimReader(db ethdb.ReversibleKeyValueStore, height uint64) *SlimReader {
	return &SlimReader{
		db:            db,
		height:        height,
		accountCache:  lru.NewCache[common.Address, *types.StateAccount](accountCacheSize),
		storageCache:  lru.NewGroupCache[common.Address, common.Hash, common.Hash](storageCacheSize),
		codeCache:     lru.NewSizeConstrainedCache[common.Hash, []byte](codeCacheSize),
		codeSizeCache: lru.NewCache[common.Hash, int](codeSizeCacheSize),
	}
}

func (r *SlimReader) Account(addr common.Address) (*types.StateAccount, error) {
	if account, ok := r.accountCache.Get(addr); ok {
		return account.Copy(), nil
	}
	data := rawdb.ReadAccount(r.db, addr, r.height)
	if len(data) == 0 {
		return nil, nil
	}
	account, err := types.FullAccount(data)
	if err != nil {
		return nil, err
	}
	r.accountCache.Add(addr, account)
	return account, nil
}

func (r *SlimReader) Storage(addr common.Address, slot common.Hash) (common.Hash, error) {
	if value, ok := r.storageCache.Get(addr, slot); ok {
		return value, nil
	}
	data := rawdb.ReadStorage(r.db, addr, slot, r.height)
	value := common.BytesToHash(data)
	r.storageCache.Add(addr, slot, value)
	return value, nil
}

func (r *SlimReader) Code(addr common.Address, codeHash common.Hash) ([]byte, error) {
	code, _ := r.codeCache.Get(codeHash)
	if len(code) > 0 {
		return code, nil
	}
	code = rawdb.ReadCode(r.db, codeHash)
	if len(code) > 0 {
		r.codeCache.Add(codeHash, code)
		r.codeSizeCache.Add(codeHash, len(code))
	}
	return code, nil
}

func (r *SlimReader) CodeSize(addr common.Address, codeHash common.Hash) (int, error) {
	if cached, ok := r.codeSizeCache.Get(codeHash); ok {
		return cached, nil
	}
	code, err := r.Code(addr, codeHash)
	if err != nil {
		return 0, err
	}
	return len(code), nil
}

func (r *SlimReader) SetHeight(height uint64) {
	if r.height == height {
		return
	}
	r.accountCache.Purge()
	r.storageCache.Purge()
	r.height = height
}

func (r *SlimReader) WriteAccount(addr common.Address, account *types.StateAccount) {
	r.accountCache.Add(addr, account.Copy())
}

func (r *SlimReader) DeleteAccount(addr common.Address) {
	r.accountCache.Remove(addr)
	r.storageCache.RemoveGroup(addr)
}

func (r *SlimReader) WriteStorage(addr common.Address, key, value common.Hash) {
	r.storageCache.Add(addr, key, value)
}

func (r *SlimReader) Commit(height uint64) {
	r.height = height
}
