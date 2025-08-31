package hashpool

import (
	"math/big"
	"sync"

	"github.com/dgraph-io/badger/v4"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eccb"
	"github.com/ethereum/go-ethereum/event"
	"github.com/holiman/uint256"
)

type HashPool struct {
	txs    map[common.Hash]*types.Transaction
	nonces map[common.Address]uint64
	lock   sync.RWMutex
	txFeed event.Feed

	db *badger.DB
}

func New(db *badger.DB) *HashPool {
	return &HashPool{
		txs:    make(map[common.Hash]*types.Transaction),
		nonces: make(map[common.Address]uint64),
		db:     db,
	}
}

func (pool *HashPool) add(tx *types.Transaction) error {
	from := *tx.To()
	pool.txs[tx.Hash()] = tx
	pool.nonces[from] = max(pool.nonces[from], tx.Nonce()+1)
	return nil
}

func (pool *HashPool) clear() {
	clear(pool.txs)
}

func (pool *HashPool) Filter(tx *types.Transaction) bool {
	return true
}

func (pool *HashPool) Init(gasTip uint64, head *types.Header, reserve txpool.AddressReserver) error {
	return nil
}

func (pool *HashPool) Close() error {
	return nil
}

func (pool *HashPool) Reset(oldHead, newHead *types.Header) {
	pool.lock.Lock()
	defer pool.lock.Unlock()
	pool.clear()
	txs, err := eccb.ReadTxs(pool.db, newHead.Number.Uint64()+1)
	if err != nil {
		return
	}
	for _, tx := range txs {
		pool.add(tx)
	}
}

func (pool *HashPool) SetGasTip(tip *big.Int) {}

func (pool *HashPool) Has(hash common.Hash) bool {
	pool.lock.RLock()
	defer pool.lock.RUnlock()
	_, ok := pool.txs[hash]
	return ok
}

func (pool *HashPool) Get(hash common.Hash) *types.Transaction {
	pool.lock.RLock()
	defer pool.lock.RUnlock()
	return pool.txs[hash]
}

func (pool *HashPool) Add(txs []*types.Transaction, local bool, sync bool) []error {
	pool.lock.Lock()
	defer pool.lock.Unlock()
	for _, tx := range txs {
		pool.add(tx)
	}
	return make([]error, len(txs))
}

func (pool *HashPool) Pending(filter txpool.PendingFilter) map[common.Address][]*txpool.LazyTransaction {
	pool.lock.RLock()
	defer pool.lock.RUnlock()
	pending := make(map[common.Address][]*txpool.LazyTransaction)
	for _, tx := range pool.txs {
		from := *tx.To()
		if _, ok := pending[from]; !ok {
			pending[from] = make([]*txpool.LazyTransaction, 0)
		}
		pending[from] = append(pending[from], &txpool.LazyTransaction{
			Pool:      pool,
			Hash:      tx.Hash(),
			Tx:        tx,
			Time:      tx.Time(),
			GasFeeCap: uint256.MustFromBig(tx.GasFeeCap()),
			GasTipCap: uint256.MustFromBig(tx.GasTipCap()),
			Gas:       tx.Gas(),
			BlobGas:   tx.BlobGas(),
		})
	}
	return pending
}

func (pool *HashPool) SubscribeTransactions(ch chan<- core.NewTxsEvent, reorgs bool) event.Subscription {
	return pool.txFeed.Subscribe(ch)
}

func (pool *HashPool) Nonce(addr common.Address) uint64 {
	pool.lock.RLock()
	defer pool.lock.RUnlock()
	return pool.nonces[addr]
}

func (pool *HashPool) Stats() (int, int) {
	pool.lock.RLock()
	defer pool.lock.RUnlock()
	return len(pool.txs), 0
}

func (pool *HashPool) Content() (map[common.Address][]*types.Transaction, map[common.Address][]*types.Transaction) {
	pool.lock.RLock()
	defer pool.lock.RUnlock()
	pending := make(map[common.Address][]*types.Transaction)
	for _, tx := range pool.txs {
		from := *tx.To()
		if _, ok := pending[from]; !ok {
			pending[from] = make([]*types.Transaction, 0)
		}
		pending[from] = append(pending[from], tx)
	}
	return pending, make(map[common.Address][]*types.Transaction)
}

func (pool *HashPool) ContentFrom(addr common.Address) ([]*types.Transaction, []*types.Transaction) {
	pool.lock.RLock()
	defer pool.lock.RUnlock()
	pending := make([]*types.Transaction, 0)
	for _, tx := range pool.txs {
		if *tx.To() == addr {
			pending = append(pending, tx)
		}
	}
	return pending, make([]*types.Transaction, 0)
}

func (pool *HashPool) Locals() []common.Address {
	return make([]common.Address, 0)
}

func (pool *HashPool) Status(hash common.Hash) txpool.TxStatus {
	pool.lock.RLock()
	defer pool.lock.RUnlock()
	if _, ok := pool.txs[hash]; ok {
		return txpool.TxStatusPending
	}
	return txpool.TxStatusUnknown
}
