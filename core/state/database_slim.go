package state

import (
	"slices"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state/snapshot"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/trie/utils"
	"github.com/ethereum/go-ethereum/triedb"
)

type SlimDatabase struct {
	disk   ethdb.ReversibleKeyValueStore
	reader *SlimReader
	height uint64

	accounts map[common.Address]*types.StateAccount
	deletes  map[common.Address]struct{}
	storages map[common.Address]map[common.Hash]common.Hash
	codes    map[common.Hash][]byte
}

func NewSlimDatabase(disk ethdb.ReversibleKeyValueStore) *SlimDatabase {
	reader := NewSlimReader(disk, 0)
	return &SlimDatabase{
		disk:     disk,
		reader:   reader,
		accounts: make(map[common.Address]*types.StateAccount),
		deletes:  make(map[common.Address]struct{}),
		storages: make(map[common.Address]map[common.Hash]common.Hash),
		codes:    make(map[common.Hash][]byte),
	}
}

func (db *SlimDatabase) Reader(root common.Hash) (Reader, error) {
	height := root.Big().Uint64()
	reader := db.reader
	reader.SetHeight(height)
	return reader, nil
}

func (db *SlimDatabase) OpenTrie(root common.Hash) (Trie, error) {
	panic("not supported")
}

func (db *SlimDatabase) OpenStorageTrie(stateRoot common.Hash, address common.Address, root common.Hash, trie Trie) (Trie, error) {
	panic("not supported")
}

func (db *SlimDatabase) PointCache() *utils.PointCache {
	panic("not supported")
}

func (db *SlimDatabase) TrieDB() *triedb.Database {
	panic("not supported")
}

func (db *SlimDatabase) Snapshot() *snapshot.Tree {
	panic("not supported")
}

func (db *SlimDatabase) SetHeight(height uint64) {
	if db.height == height {
		return
	}
	clear(db.accounts)
	clear(db.deletes)
	clear(db.storages)
	clear(db.codes)
	db.height = height
}

func (db *SlimDatabase) WriteAccount(addr common.Address, acct *types.StateAccount) error {
	db.accounts[addr] = acct.Copy()
	return nil
}

func (db *SlimDatabase) DeleteAccount(addr common.Address) error {
	delete(db.accounts, addr)
	delete(db.storages, addr)
	db.deletes[addr] = struct{}{}
	return nil
}

func (db *SlimDatabase) WriteStorage(addr common.Address, key, value common.Hash) error {
	if _, ok := db.storages[addr]; !ok {
		db.storages[addr] = make(map[common.Hash]common.Hash)
	}
	db.storages[addr][key] = value
	return nil
}

func (db *SlimDatabase) WriteCode(codeHash common.Hash, code []byte) error {
	db.codes[codeHash] = code
	return nil
}

func (db *SlimDatabase) Commit(height uint64) error {
	reader := db.reader
	batch := db.disk.NewBatch()
	for addr := range db.deletes {
		rawdb.DeleteAccount(batch, addr, height)
		reader.DeleteAccount(addr)
	}
	for addr, account := range db.accounts {
		var oldAccount *types.StateAccount
		if _, ok := db.deletes[addr]; !ok {
			var err error
			oldAccount, err = reader.Account(addr)
			if err != nil {
				return err
			}
		}
		if oldAccount != nil && account.Nonce == oldAccount.Nonce && account.Balance.Eq(oldAccount.Balance) &&
			account.Root == oldAccount.Root && slices.Equal(account.CodeHash, oldAccount.CodeHash) {
			continue
		}
		rawdb.WriteAccount(batch, addr, height, types.SlimAccountRLP(*account))
		reader.WriteAccount(addr, account)
	}
	for addr, storages := range db.storages {
		for key, value := range storages {
			var oldValue common.Hash
			if _, ok := db.deletes[addr]; !ok {
				var err error
				oldValue, err = reader.Storage(addr, key)
				if err != nil {
					return err
				}
			}
			if value == oldValue {
				continue
			}
			rawdb.WriteStorage(batch, addr, key, height, common.TrimLeftZeroes(value[:]))
			reader.WriteStorage(addr, key, value)
		}
	}
	for codeHash, code := range db.codes {
		rawdb.WriteCode(batch, codeHash, code)
	}
	if err := batch.Write(); err != nil {
		return err
	}
	db.height = height
	reader.Commit(height)
	clear(db.accounts)
	clear(db.deletes)
	clear(db.storages)
	clear(db.codes)
	return nil
}
