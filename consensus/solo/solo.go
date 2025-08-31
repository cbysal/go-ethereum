package solo

import (
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethereum/go-ethereum/trie"
)

type Solo struct {
	period uint64

	signer common.Address
	lock   sync.RWMutex
}

func New(config *params.SoloConfig) *Solo {
	return &Solo{
		period: config.Period,
	}
}

func (solo *Solo) Author(header *types.Header) (common.Address, error) {
	return header.Coinbase, nil
}

func (solo *Solo) VerifyHeader(chain consensus.ChainHeaderReader, header *types.Header) error {
	return nil
}

func (solo *Solo) VerifyHeaders(chain consensus.ChainHeaderReader, headers []*types.Header) (chan<- struct{}, <-chan error) {
	abort, results := make(chan struct{}), make(chan error, len(headers))
	for i := 0; i < len(headers); i++ {
		results <- nil
	}
	return abort, results
}

func (solo *Solo) VerifyUncles(chain consensus.ChainReader, block *types.Block) error {
	return nil
}

func (solo *Solo) Prepare(chain consensus.ChainHeaderReader, header *types.Header) error {
	parent := chain.GetHeader(header.ParentHash, header.Number.Uint64()-1)
	header.Difficulty = big.NewInt(1)
	if parent.Number.Uint64() == 0 {
		header.Time = uint64(time.Now().Unix()) + solo.period
	} else {
		header.Time = parent.Time + solo.period
	}
	return nil
}

func (solo *Solo) Finalize(chain consensus.ChainHeaderReader, header *types.Header, state *state.StateDB, txs []*types.Transaction, uncles []*types.Header, withdrawals []*types.Withdrawal) {
}

func (solo *Solo) FinalizeAndAssemble(chain consensus.ChainHeaderReader, header *types.Header, state *state.StateDB, txs []*types.Transaction, uncles []*types.Header, receipts []*types.Receipt, withdrawals []*types.Withdrawal) (*types.Block, error) {
	solo.Finalize(chain, header, state, txs, uncles, withdrawals)
	header.Root = state.IntermediateRoot(chain.Config().IsEIP158(header.Number))
	return types.NewBlock(header, txs, uncles, receipts, trie.NewStackTrie(nil)), nil
}

func (solo *Solo) Seal(chain consensus.ChainHeaderReader, block *types.Block, results chan<- *types.Block, stop <-chan struct{}) error {
	delay := time.Unix(int64(block.Time()), 0).Sub(time.Now())
	go func(delay time.Duration) {
		select {
		case <-stop:
			return
		case <-time.After(delay):
		}

		select {
		case results <- block:
		default:
			log.Warn("Sealing result is not read by miner")
		}
	}(delay)
	return nil
}

func (solo *Solo) SealHash(header *types.Header) common.Hash {
	return common.BigToHash(header.Number)
}

func (solo *Solo) CalcDifficulty(chain consensus.ChainHeaderReader, time uint64, parent *types.Header) *big.Int {
	return big.NewInt(1)
}

func (solo *Solo) APIs(chain consensus.ChainHeaderReader) []rpc.API {
	return []rpc.API{}
}

func (solo *Solo) Close() error {
	return nil
}
