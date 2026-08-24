// Copyright 2015 The go-ethereum Authors
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

package core

import (
	"fmt"
	"math/big"
	"slices"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/misc"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// StateProcessor is a basic Processor, which takes care of transitioning
// state from one point to another.
//
// StateProcessor implements Processor.
type StateProcessor struct {
	chain       ChainContext // Chain context interface
	GasUsedList []uint64
}

// NewStateProcessor initialises a new StateProcessor.
func NewStateProcessor(chain ChainContext) *StateProcessor {
	return &StateProcessor{
		chain: chain,
	}
}

// chainConfig returns the chain configuration.
func (p *StateProcessor) chainConfig() *params.ChainConfig {
	return p.chain.Config()
}

// Process processes the state changes according to the Ethereum rules by running
// the transaction messages using the statedb and applying any rewards to both
// the processor (coinbase) and any included uncles.
//
// Process returns the receipts and logs accumulated during the process and
// returns the amount of gas that was used in the process. If any of the
// transactions failed to execute due to insufficient gas it will return an error.
func (p *StateProcessor) Process(block *types.Block, statedb *state.StateDB, cfg vm.Config) (*ProcessResult, error) {
	var (
		config      = p.chainConfig()
		receipts    types.Receipts
		usedGas     = new(uint64)
		header      = block.Header()
		blockHash   = block.Hash()
		blockNumber = block.Number()
		allLogs     []*types.Log
		gp          = new(GasPool).AddGas(block.GasLimit() * 1000)
		statedbBase = statedb
	)

	// Mutate the block and state according to any hard-fork specs
	if config.DAOForkSupport && config.DAOForkBlock != nil && config.DAOForkBlock.Cmp(block.Number()) == 0 {
		misc.ApplyDAOHardFork(statedb)
	}
	var (
		context vm.BlockContext
		signer  = types.MakeSigner(config, header.Number, header.Time)
	)

	// Apply pre-execution system calls.
	var tracingStateDB = vm.StateDB(statedb)
	if hooks := cfg.Tracer; hooks != nil {
		tracingStateDB = state.NewHookedState(statedb, hooks)
	}
	context = NewEVMBlockContext(header, p.chain, nil)
	evm := vm.NewEVM(context, tracingStateDB, config, cfg)

	if beaconRoot := block.BeaconRoot(); beaconRoot != nil {
		evm.Config.IsSeer = false
		evm.Config.IsPreExec = false
		ProcessBeaconBlockRoot(*beaconRoot, evm)
		evm.Config.IsSeer = cfg.IsSeer
		evm.Config.IsPreExec = cfg.IsPreExec
	}
	if config.IsPrague(block.Number(), block.Time()) || config.IsVerkle(block.Number(), block.Time()) {
		evm.Config.IsSeer = false
		evm.Config.IsPreExec = false
		ProcessParentBlockHash(block.ParentHash(), evm)
		evm.Config.IsSeer = cfg.IsSeer
		evm.Config.IsPreExec = cfg.IsPreExec
	}

	// Iterate over and process the individual transactions
	var txs types.Transactions
	if !cfg.IsPreExec {
		txs = block.Transactions()
	} else {
		txs = make(types.Transactions, 0, block.Transactions().Len())
		for i, tx := range block.Transactions() {
			if !cfg.Privates.Test(uint(i)) {
				txs = append(txs, tx)
			}
		}
		slices.SortFunc(txs, func(a, b *types.Transaction) int {
			return -a.EffectiveGasTipCmp(b, uint256.MustFromBig(block.BaseFee()))
		})
	}
	for i, tx := range txs {
		msg, err := TransactionToMessage(tx, signer, header.BaseFee)
		if err != nil {
			return nil, fmt.Errorf("could not apply tx %d [%v]: %w", i, tx.Hash().Hex(), err)
		}
		statedb.SetTxContext(tx.Hash(), i)

		var preResult *vm.Result
		if cfg.IsSeer {
			if cfg.IsPreExec {
				statedb = statedbBase.Copy()
				evm.StateDB = statedb
				cfg.PreExecutionTable.InitialResult(tx.Hash())
			} else {
				preResult, _ = cfg.PreExecutionTable.GetResult(tx.Hash())
			}
			statedb.SetNonce(msg.From, msg.Nonce, tracing.NonceChangeUnspecified)
			mgval := new(big.Int).SetUint64(msg.GasLimit)
			mgval = mgval.Mul(mgval, msg.GasPrice)
			balanceCheck := mgval
			if msg.GasFeeCap != nil {
				balanceCheck = new(big.Int).SetUint64(msg.GasLimit)
				balanceCheck = balanceCheck.Mul(balanceCheck, msg.GasFeeCap)
				balanceCheck.Add(balanceCheck, msg.Value)
			}
			statedb.AddBalance(msg.From, uint256.MustFromBig(balanceCheck), tracing.BalanceChangeUnspecified)
		}

		receipt, err := ApplyTransactionWithEVM(msg, gp, statedb, blockNumber, blockHash, context.Time, tx, usedGas, evm, preResult)
		if cfg.IsPreExec {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("could not apply tx %d [%v]: %w", i, tx.Hash().Hex(), err)
		}
		receipts = append(receipts, receipt)
		allLogs = append(allLogs, receipt.Logs...)
	}
	if cfg.IsSeer {
		if cfg.IsPreExec {
			return nil, nil
		}
		for i, tx := range block.Transactions() {
			if cfg.Privates.Test(uint(i)) {
				continue
			}
			ret, _ := cfg.PreExecutionTable.GetResult(tx.Hash())
			brs := ret.GetBranches()
			if len(brs) > 0 {
				for _, br := range brs {
					sUnit, _ := br.GetStateUnit().(*vm.StateUnit)
					if br.GetFilled() && strings.Compare(sUnit.GetBlockEnv(), "nil") == 0 {
						if err := cfg.VarTable.AddHistory(br); err != nil {
							return nil, err
						}
					}
				}
			}
		}
	}
	// Read requests if Prague is enabled.
	var requests [][]byte
	if config.IsPrague(block.Number(), block.Time()) {
		evm.Config.IsSeer = false
		evm.Config.IsPreExec = false
		evm.SetCallMap(nil)
		requests = [][]byte{}
		// EIP-6110
		if err := ParseDepositLogs(&requests, allLogs, config); err != nil {
			return nil, fmt.Errorf("failed to parse deposit logs: %w", err)
		}
		// EIP-7002
		if err := ProcessWithdrawalQueue(&requests, evm); err != nil {
			return nil, fmt.Errorf("failed to process withdrawal queue: %w", err)
		}
		// EIP-7251
		if err := ProcessConsolidationQueue(&requests, evm); err != nil {
			return nil, fmt.Errorf("failed to process consolidation queue: %w", err)
		}
		evm.Config.IsSeer = cfg.IsSeer
		evm.Config.IsPreExec = cfg.IsPreExec
	}

	// Finalize the block, applying any consensus engine specific extras (e.g. block rewards)
	p.chain.Engine().Finalize(p.chain, header, tracingStateDB, block.Body())

	return &ProcessResult{
		Receipts: receipts,
		Requests: requests,
		Logs:     allLogs,
		GasUsed:  *usedGas,
	}, nil
}

func ApplyMessageHelper(evm *vm.EVM, msg *Message, gp *GasPool, statedb *state.StateDB, tx *types.Transaction, preResult *vm.Result) (result *ExecutionResult, err error) {
	evm.SetCallMap(nil)
	if preResult == nil || tx.To() == nil || evm.StateDB.GetCodeSize(*tx.To()) == 0 || evm.Config.Privates.Test(uint(statedb.TxIndex())) {
		result, err = ApplyMessage(evm, msg, gp, false, 0, nil)
		if err != nil {
			return nil, err
		}
	} else {
		evm.SetTxContext(NewEVMTxContext(msg))

		var isBreak bool
		brs := preResult.GetBranches()
		for _, br := range brs {
			// update the sstore info
			sstores := br.GetSstoreInfo()
			for _, sstore := range sstores {
				if err = updateSstore(sstore, evm); err != nil {
					return nil, err
				}
			}

			isTaken, err := checkBranchInExecution(evm, tx, br)
			if err != nil {
				return nil, err
			}

			isAccurate := isTaken == br.GetBranchDirection()

			if !isAccurate {
				// encounter inconsistent path, conduct fast-path repair
				callMap := make(map[int]*vm.Snapshot)
				stackElement := uint256.Int{}
				curSnapshots := br.GetSnapshots()
				callStack := preResult.GetCallStack()
				latestSnapshot := curSnapshots[len(curSnapshots)-1]
				if len(callStack) > 0 {
					// internal call exists
					for depth, sps := range callStack {
						// put the latest snapshot under each depth into the call map
						if depth == latestSnapshot.GetDepth() {
							callMap[depth] = latestSnapshot
							continue
						}
						callMap[depth] = sps[len(sps)-1]
					}
				} else {
					callMap[1] = latestSnapshot
				}

				// modify the branch info
				if isTaken {
					pc := latestSnapshot.GetPC()
					latestSnapshot.UpdatePC(pc + 1)
					br.DecideDirection(1)
					if br.GetJudgement() != "EQ" {
						stackElement.SetUint64(1)
						latestSnapshot.GetStack().UpdatePeek(stackElement)
					}
				} else {
					br.DecideDirection(0)
					if br.GetJudgement() == "EQ" {
						jumpPc := latestSnapshot.GetJumpPC()
						latestSnapshot.UpdatePC(jumpPc)
					} else {
						pc := latestSnapshot.GetPC()
						latestSnapshot.UpdatePC(pc + 1)
						stackElement.SetUint64(0)
						latestSnapshot.GetStack().UpdatePeek(stackElement)
					}
				}

				// recover execution from the initial snapshot
				evm.SetCallMap(callMap)
				gas := uint64(0)
				if _, ok := callMap[1]; ok {
					gas = callMap[1].GetContract().Gas
				} else {
					gas = callMap[2].GetContract().Gas
				}

				result, err = ApplyMessage(evm, msg, gp, true, gas, nil)
				if err != nil {
					return nil, err
				}

				isBreak = true
				break
			}
		}
		// all the branches are satisfied, execute the snapshot
		if !isBreak {
			finalSnapshot := preResult.GetFinalSnapshot()
			// in case that some contract exists without using the exist-relevant opcodes
			if finalSnapshot != nil {
				for _, sstore := range finalSnapshot.GetSstoreInfo() {
					if err = updateSstore(sstore, evm); err != nil {
						return nil, err
					}
				}
				result, err = ApplyMessage(evm, msg, gp, true, 0, finalSnapshot)
				if err != nil {
					return nil, err
				}
			} else {
				result, err = ApplyMessage(evm, msg, gp, false, 0, nil)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	return result, err
}

// ApplyTransactionWithEVM attempts to apply a transaction to the given state database
// and uses the input parameters for its environment similar to ApplyTransaction. However,
// this method takes an already created EVM instance as input.
func ApplyTransactionWithEVM(msg *Message, gp *GasPool, statedb *state.StateDB, blockNumber *big.Int, blockHash common.Hash, blockTime uint64, tx *types.Transaction, usedGas *uint64, evm *vm.EVM, preResult *vm.Result) (receipt *types.Receipt, err error) {
	if hooks := evm.Config.Tracer; hooks != nil {
		if hooks.OnTxStart != nil {
			hooks.OnTxStart(evm.GetVMContext(), tx, msg.From)
		}
		if hooks.OnTxEnd != nil {
			defer func() { hooks.OnTxEnd(receipt, err) }()
		}
	}
	// Apply the transaction to the current state (included in the env).
	result, err := ApplyMessageHelper(evm, msg, gp, statedb, tx, preResult)
	if err != nil {
		return nil, err
	}
	// Update the state with pending changes.
	var root []byte
	if evm.ChainConfig().IsByzantium(blockNumber) {
		evm.StateDB.Finalise(true)
	} else {
		root = statedb.IntermediateRoot(evm.ChainConfig().IsEIP158(blockNumber)).Bytes()
	}
	*usedGas += result.UsedGas

	// Merge the tx-local access event into the "block-local" one, in order to collect
	// all values, so that the witness can be built.
	if statedb.IsVerkle() {
		statedb.AccessEvents().Merge(evm.AccessEvents)
	}
	return MakeReceipt(evm, result, statedb, blockNumber, blockHash, blockTime, tx, *usedGas, root), nil
}

// MakeReceipt generates the receipt object for a transaction given its execution result.
func MakeReceipt(evm *vm.EVM, result *ExecutionResult, statedb *state.StateDB, blockNumber *big.Int, blockHash common.Hash, blockTime uint64, tx *types.Transaction, usedGas uint64, root []byte) *types.Receipt {
	// Create a new receipt for the transaction, storing the intermediate root and gas used
	// by the tx.
	receipt := &types.Receipt{Type: tx.Type(), PostState: root, CumulativeGasUsed: usedGas}
	if result.Failed() {
		receipt.Status = types.ReceiptStatusFailed
	} else {
		receipt.Status = types.ReceiptStatusSuccessful
	}
	receipt.TxHash = tx.Hash()
	receipt.GasUsed = result.UsedGas

	if tx.Type() == types.BlobTxType {
		receipt.BlobGasUsed = uint64(len(tx.BlobHashes()) * params.BlobTxBlobGasPerBlob)
		receipt.BlobGasPrice = evm.Context.BlobBaseFee
	}

	// If the transaction created a contract, store the creation address in the receipt.
	if tx.To() == nil {
		receipt.ContractAddress = crypto.CreateAddress(evm.TxContext.Origin, tx.Nonce())
	}

	// Set the receipt logs and create the bloom filter.
	receipt.Logs = statedb.GetLogs(tx.Hash(), blockNumber.Uint64(), blockHash, blockTime)
	receipt.Bloom = types.CreateBloom(receipt)
	receipt.BlockHash = blockHash
	receipt.BlockNumber = blockNumber
	receipt.TransactionIndex = uint(statedb.TxIndex())
	return receipt
}

// ApplyTransaction attempts to apply a transaction to the given state database
// and uses the input parameters for its environment. It returns the receipt
// for the transaction, gas used and an error if the transaction failed,
// indicating the block was invalid.
func ApplyTransaction(evm *vm.EVM, gp *GasPool, statedb *state.StateDB, header *types.Header, tx *types.Transaction, usedGas *uint64) (*types.Receipt, error) {
	msg, err := TransactionToMessage(tx, types.MakeSigner(evm.ChainConfig(), header.Number, header.Time), header.BaseFee)
	if err != nil {
		return nil, err
	}
	// Create a new context to be used in the EVM environment
	return ApplyTransactionWithEVM(msg, gp, statedb, header.Number, header.Hash(), header.Time, tx, usedGas, evm, nil)
}

// ProcessBeaconBlockRoot applies the EIP-4788 system call to the beacon block root
// contract. This method is exported to be used in tests.
func ProcessBeaconBlockRoot(beaconRoot common.Hash, evm *vm.EVM) {
	if tracer := evm.Config.Tracer; tracer != nil {
		onSystemCallStart(tracer, evm.GetVMContext())
		if tracer.OnSystemCallEnd != nil {
			defer tracer.OnSystemCallEnd()
		}
	}
	msg := &Message{
		From:      params.SystemAddress,
		GasLimit:  30_000_000,
		GasPrice:  common.Big0,
		GasFeeCap: common.Big0,
		GasTipCap: common.Big0,
		To:        &params.BeaconRootsAddress,
		Data:      beaconRoot[:],
	}
	evm.SetTxContext(NewEVMTxContext(msg))
	evm.StateDB.AddAddressToAccessList(params.BeaconRootsAddress)
	_, _, _ = evm.Call(msg.From, *msg.To, msg.Data, 30_000_000, common.U2560)
	evm.StateDB.Finalise(true)
}

// ProcessParentBlockHash stores the parent block hash in the history storage contract
// as per EIP-2935/7709.
func ProcessParentBlockHash(prevHash common.Hash, evm *vm.EVM) {
	if tracer := evm.Config.Tracer; tracer != nil {
		onSystemCallStart(tracer, evm.GetVMContext())
		if tracer.OnSystemCallEnd != nil {
			defer tracer.OnSystemCallEnd()
		}
	}
	msg := &Message{
		From:      params.SystemAddress,
		GasLimit:  30_000_000,
		GasPrice:  common.Big0,
		GasFeeCap: common.Big0,
		GasTipCap: common.Big0,
		To:        &params.HistoryStorageAddress,
		Data:      prevHash.Bytes(),
	}
	evm.SetTxContext(NewEVMTxContext(msg))
	evm.StateDB.AddAddressToAccessList(params.HistoryStorageAddress)
	_, _, err := evm.Call(msg.From, *msg.To, msg.Data, 30_000_000, common.U2560)
	if err != nil {
		panic(err)
	}
	if evm.StateDB.AccessEvents() != nil {
		evm.StateDB.AccessEvents().Merge(evm.AccessEvents)
	}
	evm.StateDB.Finalise(true)
}

// ProcessWithdrawalQueue calls the EIP-7002 withdrawal queue contract.
// It returns the opaque request data returned by the contract.
func ProcessWithdrawalQueue(requests *[][]byte, evm *vm.EVM) error {
	return processRequestsSystemCall(requests, evm, 0x01, params.WithdrawalQueueAddress)
}

// ProcessConsolidationQueue calls the EIP-7251 consolidation queue contract.
// It returns the opaque request data returned by the contract.
func ProcessConsolidationQueue(requests *[][]byte, evm *vm.EVM) error {
	return processRequestsSystemCall(requests, evm, 0x02, params.ConsolidationQueueAddress)
}

func processRequestsSystemCall(requests *[][]byte, evm *vm.EVM, requestType byte, addr common.Address) error {
	if tracer := evm.Config.Tracer; tracer != nil {
		onSystemCallStart(tracer, evm.GetVMContext())
		if tracer.OnSystemCallEnd != nil {
			defer tracer.OnSystemCallEnd()
		}
	}
	msg := &Message{
		From:      params.SystemAddress,
		GasLimit:  30_000_000,
		GasPrice:  common.Big0,
		GasFeeCap: common.Big0,
		GasTipCap: common.Big0,
		To:        &addr,
	}
	evm.SetTxContext(NewEVMTxContext(msg))
	evm.StateDB.AddAddressToAccessList(addr)
	ret, _, err := evm.Call(msg.From, *msg.To, msg.Data, 30_000_000, common.U2560)
	evm.StateDB.Finalise(true)
	if err != nil {
		return fmt.Errorf("system call failed to execute: %v", err)
	}
	if len(ret) == 0 {
		return nil // skip empty output
	}
	// Append prefixed requestsData to the requests list.
	requestsData := make([]byte, len(ret)+1)
	requestsData[0] = requestType
	copy(requestsData[1:], ret)
	*requests = append(*requests, requestsData)
	return nil
}

var depositTopic = common.HexToHash("0x649bbc62d0e31342afea4e5cd82d4049e7e1ee912fc0889aa790803be39038c5")

// ParseDepositLogs extracts the EIP-6110 deposit values from logs emitted by
// BeaconDepositContract.
func ParseDepositLogs(requests *[][]byte, logs []*types.Log, config *params.ChainConfig) error {
	deposits := make([]byte, 1) // note: first byte is 0x00 (== deposit request type)
	for _, log := range logs {
		if log.Address == config.DepositContractAddress && len(log.Topics) > 0 && log.Topics[0] == depositTopic {
			request, err := types.DepositLogToRequest(log.Data)
			if err != nil {
				return fmt.Errorf("unable to parse deposit data: %v", err)
			}
			deposits = append(deposits, request...)
		}
	}
	if len(deposits) > 1 {
		*requests = append(*requests, deposits)
	}
	return nil
}

func onSystemCallStart(tracer *tracing.Hooks, ctx *tracing.VMContext) {
	if tracer.OnSystemCallStartV2 != nil {
		tracer.OnSystemCallStartV2(ctx)
	} else if tracer.OnSystemCallStart != nil {
		tracer.OnSystemCallStart()
	}
}

// checkBranchInExecution checks whether the current state satisfies the stored branch info to perform quick path (during execution)
func checkBranchInExecution(evm *vm.EVM, tx *types.Transaction, branch *vm.BranchContext) (bool, error) {
	// do not encounter any branches under the last snapshot
	if !branch.GetFilled() {
		return branch.GetBranchDirection(), nil
	}

	var (
		firstVal, secondVal uint256.Int
		compact, compact2   bool
		err                 error
	)
	su, _ := branch.GetStateUnit().(*vm.StateUnit)
	slot := su.GetSlot()
	offset := su.GetOffset()
	bits := su.GetBits()
	if offset.Uint64() > 0 && bits < 256 {
		compact = true
	}

	firstVal, err = vm.GetComparedVal(evm, tx.Hash(), branch.GetAddr(), slot, offset, bits, su, compact, false)
	if err != nil {
		return false, err
	}

	tracingUnit := branch.GetTracingUnit()
	if branch.IsVar() {
		cUnit, _ := tracingUnit.(*vm.StateUnit)
		slot2 := cUnit.GetSlot()
		offset2 := cUnit.GetOffset()
		bits2 := cUnit.GetBits()
		if offset2.Uint64() > 0 && bits2 < 256 {
			compact2 = true
		}
		// utilizes the stateDB to check
		secondVal, err = vm.GetComparedVal(evm, tx.Hash(), branch.GetAddr(), slot2, offset2, bits2, cUnit, compact2, false)
		if err != nil {
			return false, err
		}
	} else {
		secondVal = tracingUnit.GetValue()
	}

	// compute the current branch direction
	direction := branch.GetJudgementDirection()
	judgement := vm.StringToOp(branch.GetJudgement())
	if direction {
		vm.Compute(&firstVal, &secondVal, judgement)
		if secondVal.Uint64() == 1 {
			return true, nil
		}
	} else {
		vm.Compute(&secondVal, &firstVal, judgement)
		if firstVal.Uint64() == 1 {
			return true, nil
		}
	}
	return false, nil
}

// updateSstore re-computes the stored value according to the cached sstore info
func updateSstore(sstore *vm.SstoreInfo, evm *vm.EVM) error {
	contractAddr := sstore.GetCallerAddr()
	locUnit := sstore.GetLocUnit()
	valUnit := sstore.GetValUnit()
	loc := locUnit.GetValue()
	originalVal := sstore.GetUpdatedValue()
	if sunit, ok := valUnit.(*vm.StateUnit); ok {
		if sstore.GetCompact() {
			newVal, err := computeCompactedVar(evm, evm.TxContext.ID, contractAddr, originalVal, sunit)
			if err != nil {
				return err
			}
			compactedSstore(evm, contractAddr, loc, newVal, sunit)
		} else {
			newVal, err := vm.GetComparedVal(evm, evm.TxContext.ID, contractAddr, sunit.GetSlot(), uint256.Int{}, 256, sunit, false, false)
			if err != nil {
				return err
			}
			evm.StateDB.SetState(contractAddr, loc.Bytes32(), newVal.Bytes32())
		}
	} else {
		// 直接赋值，在真正执行时，直接存储到stateDB
		evm.StateDB.SetState(contractAddr, loc.Bytes32(), originalVal.Bytes32())
	}
	return nil
}

// computeCompactedVar computes the latest stored value of a state variable when storage is compacted
func computeCompactedVar(evm *vm.EVM, txID common.Hash, contractAddr common.Address, originalVal uint256.Int, su *vm.StateUnit) (uint256.Int, error) {
	if tracers := su.GetTracer(); len(tracers) > 0 {
		lastTr := tracers[len(tracers)-1]
		if lastTr.GetOps() == vm.OR {
			unit := lastTr.GetAttaching()
			if sunit, ok := unit.(*vm.StateUnit); ok {
				var compact bool
				sunit.DeleteLastOp() // delete the mul operation
				slot := sunit.GetSlot()
				offset := sunit.GetOffset()
				bits := sunit.GetBits()
				if offset.Uint64() > 0 && bits < 256 {
					compact = true
				}
				return vm.GetComparedVal(evm, txID, contractAddr, slot, offset, bits, sunit, compact, false)
			}
		}
	}
	return originalVal, nil
}

// compactedSstore performs compacted sstore operation based on the latest value of a state variable
func compactedSstore(evm *vm.EVM, contractAddr common.Address, loc, newVal uint256.Int, sunit *vm.StateUnit) {
	var (
		res1 = new(uint256.Int)
		res2 = new(uint256.Int)
	)
	val := evm.StateDB.GetState(contractAddr, loc.Bytes32())
	valu := new(uint256.Int)
	valu.SetBytes(val.Bytes())
	offset := sunit.GetOffset()
	bits := sunit.GetBits()
	mask := vm.MakeMask(bits)
	// 计算第一部分
	res1.Mul(mask, &offset)
	res1.Not(res1)
	res1.And(res1, valu)
	// 计算第二部分
	if sunit.GetSignExtend() {
		opVal := new(uint256.Int)
		opVal.SetUint64(uint64(bits/8 - 1))
		newVal.ExtendSign(&newVal, opVal)
	}
	res2.And(mask, &newVal)
	res2.Mul(res2, &offset)
	// 两部分Or操作
	res2.Or(res2, res1)
	evm.StateDB.SetState(contractAddr, loc.Bytes32(), res2.Bytes32())
}
