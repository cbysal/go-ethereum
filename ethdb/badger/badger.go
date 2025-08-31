package badger

import (
	"bytes"
	"errors"
	"math/big"
	"sort"
	"strings"
	"sync"
	"unsafe"

	"github.com/dgraph-io/badger/v4"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/rlp"
)

var (
	errNotFound = errors.New("not found")
)

var (
	headerPrefix   = []byte("h")
	headerTDSuffix = []byte("t")

	blockBodyPrefix = []byte("b")
)

func isHeaderKey(key []byte) bool {
	return bytes.HasPrefix(key, headerPrefix) && len(key) == (len(headerPrefix)+8+common.HashLength)
}

func isBodyKey(key []byte) bool {
	return bytes.HasPrefix(key, blockBodyPrefix) && len(key) == (len(blockBodyPrefix)+8+common.HashLength)
}

func isTD(key []byte) bool {
	return bytes.HasPrefix(key, headerPrefix) && bytes.HasSuffix(key, headerTDSuffix)
}

type Database struct {
	fn     string
	memory map[string][]byte
	lock   sync.RWMutex
}

func New(file string) (*Database, error) {
	db := &Database{
		fn:     file,
		memory: make(map[string][]byte),
	}

	bdb, err := badger.Open(badger.DefaultOptions(file).WithLoggingLevel(badger.ERROR))
	if err != nil {
		return nil, err
	}
	defer bdb.Close()

	if err = bdb.View(func(txn *badger.Txn) error {
		iter := txn.NewIterator(badger.DefaultIteratorOptions)
		defer iter.Close()
		for iter.Rewind(); iter.Valid(); iter.Next() {
			item := iter.Item()
			key := item.Key()
			if err = item.Value(func(value []byte) error {
				switch {
				case isHeaderKey(key):
					header := new(types.Header)
					if err = rlp.DecodeBytes(value, header); err != nil {
						return err
					}
					db.memory[string(key)] = unsafe.Slice((*byte)(unsafe.Pointer(header)), 0)
				case isBodyKey(key):
					body := new(types.Body)
					if err = rlp.DecodeBytes(value, body); err != nil {
						return err
					}
					db.memory[string(key)] = unsafe.Slice((*byte)(unsafe.Pointer(body)), 0)
				case isTD(key):
					td := new(big.Int)
					if err = rlp.DecodeBytes(value, td); err != nil {
						return err
					}
					db.memory[string(key)] = unsafe.Slice((*byte)(unsafe.Pointer(td)), 0)
				default:
					db.memory[string(key)] = common.CopyBytes(value)
				}
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	return db, nil
}

func (db *Database) Close() error {
	bdb, err := badger.Open(badger.DefaultOptions(db.fn).WithLoggingLevel(badger.ERROR))
	if err != nil {
		return err
	}
	defer bdb.Close()

	b := bdb.NewWriteBatch()
	for key, value := range db.memory {
		keyBytes := []byte(key)
		if value == nil {
			if err = b.Delete(keyBytes); err != nil {
				return err
			}
		}
		var valueBytes []byte
		switch {
		case isHeaderKey(keyBytes):
			header := (*types.Header)(unsafe.Pointer(unsafe.SliceData(value)))
			valueBytes, err = rlp.EncodeToBytes(header)
			if err != nil {
				return err
			}
		case isBodyKey(keyBytes):
			body := new(types.Body)
			valueBytes, err = rlp.EncodeToBytes(body)
			if err != nil {
				return err
			}
		case isTD(keyBytes):
			td := new(big.Int)
			valueBytes, err = rlp.EncodeToBytes(td)
			if err != nil {
				return err
			}
		default:
			valueBytes = value
		}
		if err = b.Set(keyBytes, valueBytes); err != nil {
			return err
		}
	}
	return b.Flush()
}

func (db *Database) Has(key []byte) (bool, error) {
	db.lock.RLock()
	defer db.lock.RUnlock()

	if value, ok := db.memory[string(key)]; ok && value != nil {
		return true, nil
	}
	return false, nil
}

func (db *Database) Get(key []byte) ([]byte, error) {
	db.lock.RLock()
	defer db.lock.RUnlock()

	if entry, ok := db.memory[string(key)]; ok && entry != nil {
		return entry, nil
	}
	return nil, errNotFound
}

func (db *Database) Put(key []byte, value []byte) error {
	db.lock.Lock()
	defer db.lock.Unlock()

	db.memory[string(key)] = value
	return nil
}

func (db *Database) Delete(key []byte) error {
	db.lock.Lock()
	defer db.lock.Unlock()

	db.memory[string(key)] = nil
	return nil
}

func (db *Database) NewBatch() ethdb.Batch {
	return &batch{
		db: db,
	}
}

func (db *Database) NewBatchWithSize(size int) ethdb.Batch {
	return &batch{
		db: db,
	}
}

func (db *Database) NewIterator(prefix []byte, start []byte) ethdb.Iterator {
	db.lock.RLock()
	defer db.lock.RUnlock()

	var (
		pr     = string(prefix)
		st     = string(append(prefix, start...))
		keys   = make([]string, 0, len(db.memory))
		values = make([][]byte, 0, len(db.memory))
	)
	for key, value := range db.memory {
		if value == nil || !strings.HasPrefix(key, pr) {
			continue
		}
		if key >= st {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		values = append(values, db.memory[key])
	}
	return &iterator{
		index:  -1,
		keys:   keys,
		values: values,
	}
}

func (db *Database) NewSnapshot() (ethdb.Snapshot, error) {
	db.lock.RLock()
	defer db.lock.RUnlock()

	copied := make(map[string][]byte, len(db.memory))
	for key, val := range db.memory {
		if val == nil {
			continue
		}
		copied[key] = val
	}
	return &snapshot{db: copied}, nil
}

func (db *Database) Stat(property string) (string, error) {
	return "", errors.New("unknown property")
}

func (db *Database) Compact(start []byte, limit []byte) error {
	return nil
}

type keyvalue struct {
	key   string
	value []byte
}

type batch struct {
	db     *Database
	writes []keyvalue
	size   int
}

func (b *batch) Put(key, value []byte) error {
	b.writes = append(b.writes, keyvalue{string(key), value})
	b.size += len(key)
	return nil
}

func (b *batch) Delete(key []byte) error {
	b.writes = append(b.writes, keyvalue{string(key), nil})
	b.size += len(key)
	return nil
}

func (b *batch) ValueSize() int {
	return b.size
}

func (b *batch) Write() error {
	b.db.lock.Lock()
	defer b.db.lock.Unlock()

	for _, keyvalue := range b.writes {
		b.db.memory[keyvalue.key] = keyvalue.value
	}
	return nil
}

func (b *batch) Reset() {
	b.writes = b.writes[:0]
	b.size = 0
}

func (b *batch) Replay(w ethdb.KeyValueWriter) error {
	for _, keyvalue := range b.writes {
		if keyvalue.value == nil {
			if err := w.Delete([]byte(keyvalue.key)); err != nil {
				return err
			}
			continue
		}
		if err := w.Put([]byte(keyvalue.key), keyvalue.value); err != nil {
			return err
		}
	}
	return nil
}

type iterator struct {
	index  int
	keys   []string
	values [][]byte
}

func (it *iterator) Next() bool {
	if it.index >= len(it.keys) {
		return false
	}
	it.index++
	return it.index < len(it.keys)
}

func (it *iterator) Error() error {
	return nil
}

func (it *iterator) Key() []byte {
	if it.index < 0 || it.index >= len(it.keys) {
		return nil
	}
	return []byte(it.keys[it.index])
}

func (it *iterator) Value() []byte {
	if it.index < 0 || it.index >= len(it.keys) {
		return nil
	}
	return it.values[it.index]
}

func (it *iterator) Release() {
	it.index, it.keys, it.values = -1, nil, nil
}

type snapshot struct {
	db   map[string][]byte
	lock sync.RWMutex
}

func (snap *snapshot) Has(key []byte) (bool, error) {
	snap.lock.RLock()
	defer snap.lock.RUnlock()

	_, ok := snap.db[string(key)]
	return ok, nil
}

func (snap *snapshot) Get(key []byte) ([]byte, error) {
	snap.lock.RLock()
	defer snap.lock.RUnlock()

	if entry, ok := snap.db[string(key)]; ok {
		return entry, nil
	}
	return nil, errNotFound
}

func (snap *snapshot) Release() {
	snap.lock.Lock()
	defer snap.lock.Unlock()

	snap.db = nil
}
