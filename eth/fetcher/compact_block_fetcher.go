package fetcher

import (
	"math/rand"
	"slices"
	"time"

	"github.com/cbysal/go-interval"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/lru"
	"github.com/ethereum/go-ethereum/common/prque"
	"github.com/ethereum/go-ethereum/conf"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eccb"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/golang/snappy"
	"github.com/klauspost/reedsolomon"
)

type transactionRetrievalFn func(common.Hash) *types.Transaction
type blockCheckFn func(hash common.Hash, uint65 uint64) bool

type blockTransactionsRequesterFn func(blockHash common.Hash, height uint64, txHashes []common.Hash, sink chan *eth.Response) (*eth.Request, error)
type compactBlockRequesterFn func(common.Hash, uint64, chan *eth.Response) (*eth.Request, error)
type chunksRequesterFn func(blockHash common.Hash, height uint64, chunkSize uint64, chunkIds []uint64, sink chan *eth.Response) (*eth.Request, error)

type compactBlockAnnounce struct {
	origin string

	hash   common.Hash
	height uint64

	fetchCompactBlock      compactBlockRequesterFn
	fetchBlockTransactions blockTransactionsRequesterFn
	fetchChunks            chunksRequesterFn
}

type compactBlockBroadcast struct {
	origin string

	compactBlock  *eccb.CompactBlock
	txs           types.Transactions
	matchChunkNum uint64
	chunks        [][]byte

	fetchBlockTransactions blockTransactionsRequesterFn
	fetchChunks            chunksRequesterFn
}

type CompactBlockFetcher struct {
	blockFetcher *BlockFetcher

	announce  chan *compactBlockAnnounce
	broadcast chan *compactBlockBroadcast
	fetching  chan *compactBlockBroadcast
	enqueue   chan common.Hash

	announces  *prque.Prque[int64, *compactBlockAnnounce]
	broadcasts *prque.Prque[int64, *compactBlockBroadcast]
	fetchings  *prque.Prque[int64, *compactBlockBroadcast]

	enqueued lru.BasicLRU[common.Hash, struct{}]

	done chan common.Hash
	quit chan struct{}

	chainHeight chainHeightFn
	hasBlock    blockCheckFn
	getTx       transactionRetrievalFn
	dropPeer    peerDropFn
}

func NewCompactBlockFetcher(blockFetcher *BlockFetcher, chainHeight chainHeightFn, getTx transactionRetrievalFn,
	hasBlock blockCheckFn, dropPeer peerDropFn) *CompactBlockFetcher {
	return &CompactBlockFetcher{
		blockFetcher: blockFetcher,
		announce:     make(chan *compactBlockAnnounce),
		broadcast:    make(chan *compactBlockBroadcast),
		fetching:     make(chan *compactBlockBroadcast),
		enqueue:      make(chan common.Hash),
		announces:    prque.New[int64, *compactBlockAnnounce](nil),
		broadcasts:   prque.New[int64, *compactBlockBroadcast](nil),
		fetchings:    prque.New[int64, *compactBlockBroadcast](nil),
		enqueued:     lru.NewBasicLRU[common.Hash, struct{}](maxQueueDist),
		done:         make(chan common.Hash),
		quit:         make(chan struct{}),
		chainHeight:  chainHeight,
		getTx:        getTx,
		hasBlock:     hasBlock,
		dropPeer:     dropPeer,
	}
}

func (f *CompactBlockFetcher) Start() {
	go f.loop()
}

func (f *CompactBlockFetcher) Stop() {
	close(f.quit)
}

func (f *CompactBlockFetcher) Notify(peer string, hash common.Hash, number uint64,
	compactBlockFetcher compactBlockRequesterFn, fetchBlockTransactions blockTransactionsRequesterFn,
	chunksFetcher chunksRequesterFn) error {
	op := &compactBlockAnnounce{
		origin:                 peer,
		hash:                   hash,
		height:                 number,
		fetchCompactBlock:      compactBlockFetcher,
		fetchBlockTransactions: fetchBlockTransactions,
		fetchChunks:            chunksFetcher,
	}
	select {
	case f.announce <- op:
		return nil
	case <-f.quit:
		return errTerminated
	}
}

func (f *CompactBlockFetcher) Enqueue(peer string, compactBlock *eccb.CompactBlock,
	blockTransactionsFetcher blockTransactionsRequesterFn, chunksFetcher chunksRequesterFn) error {
	op := &compactBlockBroadcast{
		origin:                 peer,
		compactBlock:           compactBlock,
		fetchBlockTransactions: blockTransactionsFetcher,
		fetchChunks:            chunksFetcher,
	}
	select {
	case f.broadcast <- op:
		return nil
	case <-f.quit:
		return errTerminated
	}
}

func (f *CompactBlockFetcher) loop() {
	for {
		if !f.announces.Empty() {
			announce := f.announces.PopItem()
			go func(f *CompactBlockFetcher, peer string, hash common.Hash, height uint64,
				compactBlockFetcher compactBlockRequesterFn, blockTransactionsFetcher blockTransactionsRequesterFn,
				chunksFetcher chunksRequesterFn) {
				resCh := make(chan *eth.Response)
				req, err := compactBlockFetcher(hash, height, resCh)
				if err != nil {
					return
				}
				defer req.Close()

				timeout := time.NewTimer(2 * fetchTimeout)
				defer timeout.Stop()

				select {
				case res := <-resCh:
					res.Done <- nil
					compactBlock := res.Res.(*eth.CompactBlockResponse).CompactBlock
					if compactBlock == nil {
						return
					}
					f.broadcast <- &compactBlockBroadcast{
						origin:                 peer,
						compactBlock:           compactBlock,
						fetchBlockTransactions: blockTransactionsFetcher,
						fetchChunks:            chunksFetcher,
					}

				case <-timeout.C:
					f.dropPeer(peer)
				}
			}(f, announce.origin, announce.hash, announce.height, announce.fetchCompactBlock,
				announce.fetchBlockTransactions, announce.fetchChunks)
		}

		if !f.broadcasts.Empty() {
			broadcast := f.broadcasts.PopItem()
			go func(f *CompactBlockFetcher, peer string, compactBlock *eccb.CompactBlock,
				blockTransactionsFetcher blockTransactionsRequesterFn, chunksFetcher chunksRequesterFn) {
				txNum := len(compactBlock.TxHashes())
				if txNum == 0 {
					block := types.NewBlockWithHeader(compactBlock.Header())
					block = block.WithBody(types.Transactions{}, compactBlock.Uncles())
					block = block.WithWithdrawals(compactBlock.Withdrawals())
					block.ReceivedAt = time.Now()
					block.ReceivedFrom = peer
					f.blockFetcher.Enqueue(peer, block)
					f.enqueue <- block.Hash()
					return
				}

				var (
					txs           = make(types.Transactions, txNum)
					matchChunkNum uint64
					chunks        [][]byte
					matchBlock    = txNum == 0 || rand.Float64() < conf.MatchBlock
				)
				if len(compactBlock.TxSizes()) == 0 {
					for i, txHash := range compactBlock.TxHashes() {
						if matchBlock || i > 0 && rand.Float64() < conf.MatchTx {
							txs[i] = f.getTx(txHash)
						}
					}
					if !slices.Contains(txs, nil) {
						block := types.NewBlockWithHeader(compactBlock.Header())
						block = block.WithBody(txs, compactBlock.Uncles())
						block = block.WithWithdrawals(compactBlock.Withdrawals())
						block.ReceivedAt = time.Now()
						block.ReceivedFrom = peer
						f.blockFetcher.Enqueue(peer, block)
						f.enqueue <- block.Hash()
						return
					}
				} else {
					start := time.Now()
					chunkSize := compactBlock.ChunkSize()
					dataSize := uint64(0)
					for _, txSize := range compactBlock.TxSizes() {
						dataSize += txSize
					}
					dataChunkNum := (dataSize + chunkSize - 1) / chunkSize
					parityChunkNum := uint64(len(compactBlock.Parity())) / chunkSize

					matchTxNum := uint64(0)
					data := make([]byte, dataChunkNum*chunkSize)
					intervalSet := interval.NewIntervalSet[uint64]()
					curPos := uint64(0)
					for i, txHash := range compactBlock.TxHashes() {
						tx := f.getTx(txHash)
						if tx != nil {
							matchTxNum++
							txBytes := tx.Bytes()
							copy(data[curPos:], txBytes)
							intervalSet.Add(interval.Interval[uint64]{Begin: curPos, End: curPos + uint64(len(txBytes))})
							txs[i] = tx
						}
						curPos += compactBlock.TxSizes()[i]
					}
					intervalSet.Add(interval.Interval[uint64]{Begin: curPos, End: dataChunkNum * chunkSize})

					matchChunkNum = uint64(0)
					chunks = make([][]byte, dataChunkNum+parityChunkNum)
					for i := uint64(0); i < dataChunkNum; i++ {
						if !matchBlock && matchChunkNum >= dataChunkNum-parityChunkNum-1 {
							continue
						}
						if intervalSet.ContainsAll(interval.Interval[uint64]{Begin: i * chunkSize, End: (i + 1) * chunkSize}) {
							chunks[i] = data[i*chunkSize : (i+1)*chunkSize]
							matchChunkNum++
						}
					}
					for i := uint64(0); i < parityChunkNum; i++ {
						chunks[dataChunkNum+i] = compactBlock.Parity()[i*chunkSize : (i+1)*chunkSize]
					}
					if matchChunkNum+parityChunkNum >= dataChunkNum {
						encoder, err := reedsolomon.New(int(dataChunkNum), int(parityChunkNum),
							reedsolomon.WithFastOneParityMatrix(), reedsolomon.WithCauchyMatrix())
						if err != nil {
							return
						}
						if err = encoder.ReconstructData(chunks); err != nil {
							return
						}
						for i := uint64(0); i < dataChunkNum; i++ {
							copy(data[i*chunkSize:], chunks[i])
						}
						curPos = 0
						for i, tx := range txs {
							txSize := compactBlock.TxSizes()[i]
							decodedBytes, err := snappy.Decode(nil, data[curPos:curPos+txSize])
							if err != nil {
								return
							}
							if tx == nil {
								if err = rlp.DecodeBytes(decodedBytes, &txs[i]); err != nil {
									return
								}
							}
							curPos += txSize
						}
						block := types.NewBlockWithHeader(compactBlock.Header())
						block = block.WithBody(txs, compactBlock.Uncles())
						block = block.WithWithdrawals(compactBlock.Withdrawals())
						block.ReceivedAt = time.Now()
						block.ReceivedFrom = peer
						log.Info("Decoding", "height", compactBlock.NumberU64(), "elapsed", time.Since(start).Microseconds())
						f.blockFetcher.Enqueue(peer, block)
						f.enqueue <- block.Hash()
						return
					}
				}
				f.fetching <- &compactBlockBroadcast{
					origin:                 peer,
					compactBlock:           compactBlock,
					txs:                    txs,
					matchChunkNum:          matchChunkNum,
					chunks:                 chunks,
					fetchBlockTransactions: blockTransactionsFetcher,
					fetchChunks:            chunksFetcher,
				}
			}(f, broadcast.origin, broadcast.compactBlock, broadcast.fetchBlockTransactions, broadcast.fetchChunks)
		}

		if !f.fetchings.Empty() {
			fetching := f.fetchings.PopItem()
			go func(f *CompactBlockFetcher, peer string, compactBlock *eccb.CompactBlock, txs types.Transactions,
				matchChunkNum uint64, chunks [][]byte, fetchBlockTransactions blockTransactionsRequesterFn,
				fetchChunks chunksRequesterFn) {
				if len(compactBlock.TxSizes()) == 0 {
					txHashes := compactBlock.TxHashes()
					missTxHashes := make([]common.Hash, 0, len(txHashes))
					for i, tx := range txs {
						if tx == nil {
							missTxHashes = append(missTxHashes, txHashes[i])
						}
					}
					resCh := make(chan *eth.Response)
					req, err := fetchBlockTransactions(compactBlock.Hash(), compactBlock.NumberU64(), missTxHashes, resCh)
					if err != nil {
						return
					}
					defer req.Close()

					timeout := time.NewTimer(2 * fetchTimeout)
					defer timeout.Stop()

					select {
					case res := <-resCh:
						res.Done <- nil
						fetchedTxs := types.Transactions(*res.Res.(*eth.BlockTransactionsResponse))
						txsMap := make(map[common.Hash]*types.Transaction, len(fetchedTxs))
						for _, tx := range fetchedTxs {
							txsMap[tx.Hash()] = tx
						}
						for i, txHash := range compactBlock.TxHashes() {
							if txs[i] == nil {
								txs[i] = txsMap[txHash]
							}
						}
						if slices.Contains(txs, nil) {
							return
						}

					case <-timeout.C:
						f.dropPeer(peer)
					}
				} else {
					chunkSize := compactBlock.ChunkSize()
					dataSize := uint64(0)
					for _, txSize := range compactBlock.TxSizes() {
						dataSize += txSize
					}
					dataChunkNum := (dataSize + chunkSize - 1) / chunkSize
					parityChunkNum := uint64(len(compactBlock.Parity())) / chunkSize

					missChunkIds := make([]uint64, 0, dataChunkNum-parityChunkNum-matchChunkNum)
					for i, chunk := range chunks {
						if chunk == nil {
							missChunkIds = append(missChunkIds, uint64(i))
							if uint64(len(missChunkIds)) == dataChunkNum-parityChunkNum-matchChunkNum {
								break
							}
						}
					}
					resCh := make(chan *eth.Response)
					req, err := fetchChunks(compactBlock.Hash(), compactBlock.NumberU64(), chunkSize, missChunkIds, resCh)
					if err != nil {
						return
					}
					defer req.Close()

					timeout := time.NewTimer(2 * fetchTimeout)
					defer timeout.Stop()

					select {
					case res := <-resCh:
						res.Done <- nil
						fetchedChunks := *res.Res.(*eth.ChunksResponse)
						if len(fetchedChunks) == 0 {
							return
						}
						start := time.Now()
						index := 0
						for i, chunk := range chunks {
							if chunk == nil {
								chunks[i] = fetchedChunks[index]
								index++
								if uint64(index) == dataChunkNum-parityChunkNum-matchChunkNum {
									break
								}
							}
						}
						encoder, err := reedsolomon.New(int(dataChunkNum), int(parityChunkNum),
							reedsolomon.WithFastOneParityMatrix(), reedsolomon.WithCauchyMatrix())
						if err != nil {
							return
						}
						if err = encoder.ReconstructData(chunks); err != nil {
							return
						}
						data := make([]byte, dataChunkNum*chunkSize)
						for i := uint64(0); i < dataChunkNum; i++ {
							copy(data[i*chunkSize:], chunks[i])
						}
						txs = make(types.Transactions, 0, len(compactBlock.TxHashes()))
						curPos := uint64(0)
						for i := 0; i < len(compactBlock.TxHashes()); i++ {
							var tx types.Transaction
							txSize := compactBlock.TxSizes()[i]
							decodedBytes, err := snappy.Decode(nil, data[curPos:curPos+txSize])
							if err != nil {
								return
							}
							if err = rlp.DecodeBytes(decodedBytes, &tx); err != nil {
								return
							}
							txs = append(txs, &tx)
							curPos += txSize
						}
						log.Info("Decoding", "height", compactBlock.NumberU64(), "elapsed", time.Since(start).Microseconds())

					case <-timeout.C:
						f.dropPeer(peer)
					}
				}
				block := types.NewBlockWithHeader(compactBlock.Header())
				block = block.WithBody(txs, compactBlock.Uncles())
				block = block.WithWithdrawals(compactBlock.Withdrawals())
				block.ReceivedAt = time.Now()
				block.ReceivedFrom = peer
				f.blockFetcher.Enqueue(peer, block)
				f.enqueue <- block.Hash()
			}(f, fetching.origin, fetching.compactBlock, fetching.txs, fetching.matchChunkNum, fetching.chunks,
				fetching.fetchBlockTransactions, fetching.fetchChunks)
		}

		select {
		case <-f.quit:
			return

		case announce := <-f.announce:
			hash := announce.hash
			height := announce.height
			if dist := int64(height) - int64(f.chainHeight()); dist < -maxUncleDist || dist > maxQueueDist {
				continue
			}
			if f.enqueued.Contains(hash) {
				continue
			}
			if f.hasBlock(hash, height) {
				continue
			}
			f.announces.Push(announce, -int64(announce.height))

		case broadcast := <-f.broadcast:
			compactBlock := broadcast.compactBlock
			hash := compactBlock.Hash()
			height := compactBlock.NumberU64()
			if dist := int64(height) - int64(f.chainHeight()); dist < -maxUncleDist || dist > maxQueueDist {
				continue
			}
			if f.enqueued.Contains(hash) {
				continue
			}
			if f.hasBlock(hash, height) {
				continue
			}
			f.broadcasts.Push(broadcast, -int64(height))

		case fetching := <-f.fetching:
			compactBlock := fetching.compactBlock
			hash := compactBlock.Hash()
			height := compactBlock.NumberU64()
			if dist := int64(height) - int64(f.chainHeight()); dist < -maxUncleDist || dist > maxQueueDist {
				continue
			}
			if f.enqueued.Contains(hash) {
				continue
			}
			if f.hasBlock(hash, height) {
				continue
			}
			f.fetchings.Push(fetching, -int64(fetching.compactBlock.NumberU64()))

		case hash := <-f.enqueue:
			f.enqueued.Add(hash, struct{}{})
		}
	}
}
