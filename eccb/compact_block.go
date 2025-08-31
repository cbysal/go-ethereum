package eccb

import (
	"io"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/lru"
	"github.com/ethereum/go-ethereum/conf"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/klauspost/reedsolomon"
)

type extCompactBlock struct {
	Header      *types.Header
	Uncles      []*types.Header
	TxHashes    []common.Hash
	TxSizes     []uint64
	ChunkSize   uint64
	Parities    []byte
	Withdrawals []*types.Withdrawal `rlp:"optional"`
}

type CompactBlock struct {
	header      *types.Header
	uncles      []*types.Header
	txHashes    []common.Hash
	txSizes     []uint64
	chunkSize   uint64
	parity      []byte
	withdrawals types.Withdrawals

	hash atomic.Value
	size atomic.Value

	ReceivedAt   time.Time
	ReceivedFrom interface{}
}

var (
	cache = lru.NewBasicLRU[common.Hash, *CompactBlock](32)
	mu    sync.Mutex
)

func NewCompactBlock(block *types.Block, knownTxs []bool) (*CompactBlock, error) {
	mu.Lock()
	defer mu.Unlock()
	compactBlock, ok := cache.Get(block.Hash())
	if ok {
		return compactBlock, nil
	}
	txs := block.Transactions()
	txNum := txs.Len()
	if txNum == 0 || knownTxs == nil {
		compactBlock = &CompactBlock{
			header:      block.Header(),
			uncles:      block.Uncles(),
			txHashes:    make([]common.Hash, 0, txNum),
			withdrawals: block.Withdrawals(),
			txSizes:     []uint64{},
			chunkSize:   0,
			parity:      []byte{},
		}
		for _, tx := range block.Transactions() {
			compactBlock.txHashes = append(compactBlock.txHashes, tx.Hash())
		}
		cache.Add(block.Hash(), compactBlock)
		return compactBlock, nil
	}
	start := time.Now()
	dataSize, missSize := 0, 0
	txSizes := make([]uint64, txNum)
	for i := 0; i < txNum; i++ {
		txBytesLen := len(txs[i].Bytes())
		txSizes[i] = uint64(txBytesLen)
		dataSize += txBytesLen
		if !knownTxs[i] {
			missSize += txBytesLen
		}
	}
	data := make([]byte, dataSize)
	offset := 0
	for i := 0; i < txNum; i++ {
		txBytes := txs[i].Bytes()
		copy(data[offset:], txBytes)
		offset += len(txBytes)
	}
	if conf.MatchTx != 1 {
		missSize = int(float64(dataSize) * (1 - conf.MatchTx))
	}
	missSize = int(float64(missSize) * 1.014)
	chunkSize := max((dataSize+missSize)/65536, 1) * 64
	dataChunkNum := (dataSize + chunkSize - 1) / chunkSize
	parityChunkNum := max((missSize+chunkSize-1)/chunkSize, 1)
	data = append(data, make([]byte, dataChunkNum*chunkSize-dataSize)...)
	parity := make([]byte, parityChunkNum*chunkSize)
	chunks := make([][]byte, dataChunkNum+parityChunkNum)
	for i := 0; i < dataChunkNum; i++ {
		chunks[i] = data[i*chunkSize : (i+1)*chunkSize]
	}
	for i := 0; i < parityChunkNum; i++ {
		chunks[dataChunkNum+i] = parity[i*chunkSize : (i+1)*chunkSize]
	}
	encoder, err := reedsolomon.New(dataChunkNum, parityChunkNum, reedsolomon.WithFastOneParityMatrix(),
		reedsolomon.WithCauchyMatrix())
	if err != nil {
		return nil, err
	}
	if err = encoder.Encode(chunks); err != nil {
		return nil, err
	}
	compactBlock = &CompactBlock{
		header:      block.Header(),
		uncles:      block.Uncles(),
		txHashes:    make([]common.Hash, 0, block.Transactions().Len()),
		withdrawals: block.Withdrawals(),
		txSizes:     txSizes,
		chunkSize:   uint64(chunkSize),
		parity:      parity,
	}
	for _, tx := range block.Transactions() {
		compactBlock.txHashes = append(compactBlock.txHashes, tx.Hash())
	}
	log.Info("Encoding", "height", block.NumberU64(), "elapsed", time.Since(start).Microseconds())
	cache.Add(block.Hash(), compactBlock)
	return compactBlock, nil
}

func (cb *CompactBlock) DecodeRLP(s *rlp.Stream) error {
	var eEccb extCompactBlock
	_, size, _ := s.Kind()
	if err := s.Decode(&eEccb); err != nil {
		return err
	}
	cb.header, cb.uncles, cb.txHashes, cb.withdrawals, cb.txSizes, cb.chunkSize, cb.parity = eEccb.Header, eEccb.Uncles, eEccb.TxHashes, eEccb.Withdrawals, eEccb.TxSizes, eEccb.ChunkSize, eEccb.Parities
	cb.size.Store(rlp.ListSize(size))
	return nil
}

func (cb *CompactBlock) EncodeRLP(w io.Writer) error {
	return rlp.Encode(w, &extCompactBlock{
		Header:      cb.header,
		TxHashes:    cb.txHashes,
		Uncles:      cb.uncles,
		Withdrawals: cb.withdrawals,
		TxSizes:     cb.txSizes,
		ChunkSize:   cb.chunkSize,
		Parities:    cb.parity,
	})
}

func (cb *CompactBlock) Uncles() []*types.Header        { return cb.uncles }
func (cb *CompactBlock) TxHashes() []common.Hash        { return cb.txHashes }
func (cb *CompactBlock) TxSizes() []uint64              { return cb.txSizes }
func (cb *CompactBlock) ChunkSize() uint64              { return cb.chunkSize }
func (cb *CompactBlock) Parity() []byte                 { return cb.parity }
func (cb *CompactBlock) Withdrawals() types.Withdrawals { return cb.withdrawals }

func (cb *CompactBlock) Header() *types.Header {
	return types.CopyHeader(cb.header)
}

func (cb *CompactBlock) Difficulty() *big.Int {
	return new(big.Int).Set(cb.header.Difficulty)
}

func (cb *CompactBlock) NumberU64() uint64       { return cb.header.Number.Uint64() }
func (cb *CompactBlock) ParentHash() common.Hash { return cb.header.ParentHash }
func (cb *CompactBlock) UncleHash() common.Hash  { return cb.header.UncleHash }

func (cb *CompactBlock) SanityCheck() error {
	return cb.header.SanityCheck()
}

func (cb *CompactBlock) Hash() common.Hash {
	if hash := cb.hash.Load(); hash != nil {
		return hash.(common.Hash)
	}
	v := cb.header.Hash()
	cb.hash.Store(v)
	return v
}
