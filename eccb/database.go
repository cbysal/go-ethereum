package eccb

import (
	"encoding/binary"

	"github.com/dgraph-io/badger/v4"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
)

func OpenDatabase(path string, readonly bool) (*badger.DB, error) {
	option := badger.DefaultOptions(path)
	option.ReadOnly = readonly
	option.Logger = nil
	return badger.Open(option)
}

func WriteTx(db *badger.DB, tx *types.Transaction) error {
	hash := tx.Hash()
	key := hash[:]
	value, err := rlp.EncodeToBytes(tx)
	if err != nil {
		return err
	}
	txn := db.NewTransaction(true)
	if err := txn.Set(key, value); err != nil {
		return err
	}
	return txn.Commit()
}

func WriteTxs(db *badger.DB, height uint64, txs types.Transactions) error {
	hashes := make([]common.Hash, len(txs))
	for i, tx := range txs {
		hashes[i] = tx.Hash()
	}
	key := make([]byte, 8)
	binary.LittleEndian.PutUint64(key, height)
	value, err := rlp.EncodeToBytes(hashes)
	if err != nil {
		return err
	}
	txn := db.NewTransaction(true)
	if err = txn.Set(key, value); err != nil {
		return err
	}
	if err = txn.Commit(); err != nil {
		return err
	}
	for _, tx := range txs {
		if err = WriteTx(db, tx); err != nil {
			return err
		}
	}
	return nil
}

func ReadTx(db *badger.DB, hash common.Hash) (*types.Transaction, error) {
	key := hash[:]
	txn := db.NewTransaction(false)
	item, err := txn.Get(key)
	if err != nil {
		return nil, err
	}
	var tx *types.Transaction
	if err = item.Value(func(val []byte) error {
		return rlp.DecodeBytes(val, &tx)
	}); err != nil {
		return nil, err
	}
	return tx, nil
}

func ReadTxs(db *badger.DB, height uint64) (types.Transactions, error) {
	key := make([]byte, 8)
	binary.LittleEndian.PutUint64(key, height)
	txn := db.NewTransaction(false)
	item, err := txn.Get(key)
	if err != nil {
		return nil, err
	}
	var hashes []common.Hash
	if err = item.Value(func(val []byte) error {
		return rlp.DecodeBytes(val, &hashes)
	}); err != nil {
		return nil, err
	}
	txs := make(types.Transactions, len(hashes))
	for i, hash := range hashes {
		txs[i], err = ReadTx(db, hash)
		if err != nil {
			return nil, err
		}
	}
	return txs, nil
}

func CloseDatabase(db *badger.DB) error {
	return db.Close()
}
