package vm

import (
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

// PreExecutionTable stores necessary information of a pre-executed tx for subsequent actual execution
type PreExecutionTable struct {
	Space map[common.Hash]*Result
}

func NewPreExecutionTable() *PreExecutionTable {
	return &PreExecutionTable{
		Space: make(map[common.Hash]*Result),
	}
}

func (pe *PreExecutionTable) InitialResult(txID common.Hash) *Result {
	newResult := NewResult()
	pe.Space[txID] = newResult
	return newResult
}

// GetResult obtains the pre-executed results for a specific transaction
func (pe *PreExecutionTable) GetResult(txID common.Hash) (*Result, error) {
	result, ok := pe.Space[txID]
	if !ok {
		return nil, errors.New("cannot find pre-execution info")
	}
	return result, nil
}

// Result stores necessary pre-execution information for a specific tx
type Result struct {
	branchCtx     []*BranchContext
	finalSnapshot *FinalSnapshot      // record the final snapshot at the end of execution
	callStack     map[int][]*Snapshot // record the map structure from call depth to the set of corresponding snapshots
}

func NewResult() *Result {
	return &Result{
		branchCtx: make([]*BranchContext, 0, 100),
		callStack: make(map[int][]*Snapshot),
	}
}

func (rs *Result) GetBranches() []*BranchContext     { return rs.branchCtx }
func (rs *Result) GetFinalSnapshot() *FinalSnapshot  { return rs.finalSnapshot }
func (rs *Result) GetCallStack() map[int][]*Snapshot { return rs.callStack }

// CacheSStoreInfo stores sstore information and adds to the latest branch context (has not been filled with branch information)
func (rs *Result) CacheSStoreInfo(contractAddr common.Address, updatedVal uint256.Int, loc, val TracingUnit, isCompact bool) {
	if len(rs.branchCtx) > 0 {
		latestCtx := rs.branchCtx[len(rs.branchCtx)-1]
		if !latestCtx.isFilled {
			latestCtx.updateSStoreInfo(contractAddr, updatedVal, loc, val, isCompact)
			return
		}
	}
	ctx := initializeBranchContext()
	ctx.updateSStoreInfo(contractAddr, updatedVal, loc, val, isCompact)
	rs.branchCtx = append(rs.branchCtx, ctx)
}

// CacheSnapshot stores EVM snapshot and adds to the latest branch context (has not been filled with branch information)
func (rs *Result) CacheSnapshot(stack *Stack, memory *Memory, pc, jumpPc uint64, contract *Contract, depth int) {
	if len(rs.branchCtx) > 0 {
		latestCtx := rs.branchCtx[len(rs.branchCtx)-1]
		if !latestCtx.isFilled {
			snapshot := latestCtx.updateSnapshot(stack, memory, pc, jumpPc, contract, depth)
			rs.callStack[depth] = append(rs.callStack[depth], snapshot)
			return
		}
	}
	ctx := initializeBranchContext()
	snapshot := ctx.updateSnapshot(stack, memory, pc, jumpPc, contract, depth)
	rs.callStack[depth] = append(rs.callStack[depth], snapshot)
	rs.branchCtx = append(rs.branchCtx, ctx)
}

// CacheReadSet caches tx's read set during pre-execution
func (rs *Result) CacheReadSet() {
	if len(rs.branchCtx) > 0 {
		latestCtx := rs.branchCtx[len(rs.branchCtx)-1]
		if !latestCtx.isFilled {
			return
		}
	}
	ctx := initializeBranchContext()
	rs.branchCtx = append(rs.branchCtx, ctx)
}

// CacheWriteSet caches tx's write set during pre-execution
func (rs *Result) CacheWriteSet() {
	if len(rs.branchCtx) > 0 {
		latestCtx := rs.branchCtx[len(rs.branchCtx)-1]
		if !latestCtx.isFilled {
			return
		}
	}
	ctx := initializeBranchContext()
	rs.branchCtx = append(rs.branchCtx, ctx)
}

// GenerateFinalSnapshot generates a final snapshot for storing final execution results and sstore information
func (rs *Result) GenerateFinalSnapshot(result []byte, gas uint64, err error) {
	var sstoreInfo []*SstoreInfo
	if len(rs.branchCtx) > 0 {
		latestCtx := rs.branchCtx[len(rs.branchCtx)-1]
		if !latestCtx.isFilled {
			// 在执行结束前的相关sstore信息和读写集会暂时缓存在临时分支的上下文中
			// 获取sstore和读写集后将临时分支的上下文删除
			sstoreInfo = latestCtx.sstoreInfo
			rs.branchCtx = rs.branchCtx[:len(rs.branchCtx)-1]
		}
	}
	final := newFinalSnapshot(result, gas, err, sstoreInfo)
	rs.finalSnapshot = final
}

// UpdateBranchInfo updates branch info when encountering branch relevant to state variables
func (rs *Result) UpdateBranchInfo(contractAddr common.Address, sUnit, cUnit TracingUnit, isVar, direction bool, id, judgement string) {
	latestCtx := rs.branchCtx[len(rs.branchCtx)-1]
	latestCtx.addBranchInfo(contractAddr, sUnit, cUnit, isVar, direction, id, judgement)
}

// UpdateDirection updates the predicted branch direction
func (rs *Result) UpdateDirection(res int) {
	latestCtx := rs.branchCtx[len(rs.branchCtx)-1]
	latestCtx.DecideDirection(res)
}

// FinalSnapshot records final execution results and sstore information
type FinalSnapshot struct {
	ret        []byte
	gas        uint64
	err        error
	sstoreInfo []*SstoreInfo
}

func newFinalSnapshot(result []byte, gas uint64, err error, info []*SstoreInfo) *FinalSnapshot {
	return &FinalSnapshot{
		ret:        result,
		gas:        gas,
		err:        err,
		sstoreInfo: info,
	}
}

func (fs *FinalSnapshot) GetResult() []byte            { return fs.ret }
func (fs *FinalSnapshot) GetGas() uint64               { return fs.gas }
func (fs *FinalSnapshot) GetError() error              { return fs.err }
func (fs *FinalSnapshot) GetSstoreInfo() []*SstoreInfo { return fs.sstoreInfo }

// BranchContext records the necessary branch context for fast path execution
type BranchContext struct {
	branchID     string
	contractAddr common.Address // the initial caller contract address
	sUnit        TracingUnit
	cUnit        TracingUnit
	judgement    string
	isVar        bool
	isTaken      bool
	direction    bool // defines the judgement direction (x cmp. y or y cmp. x)
	snapshots    []*Snapshot
	sstoreInfo   []*SstoreInfo
	isFilled     bool // whether filled with branch information
}

func initializeBranchContext() *BranchContext {
	return &BranchContext{
		snapshots:  make([]*Snapshot, 0, 20),
		sstoreInfo: make([]*SstoreInfo, 0, 20),
		isFilled:   false,
	}
}

func (bc *BranchContext) GetBranchID() string          { return bc.branchID }
func (bc *BranchContext) GetAddr() common.Address      { return bc.contractAddr }
func (bc *BranchContext) GetStateUnit() TracingUnit    { return bc.sUnit }
func (bc *BranchContext) GetTracingUnit() TracingUnit  { return bc.cUnit }
func (bc *BranchContext) IsVar() bool                  { return bc.isVar }
func (bc *BranchContext) GetJudgement() string         { return bc.judgement }
func (bc *BranchContext) GetSnapshots() []*Snapshot    { return bc.snapshots }
func (bc *BranchContext) GetSstoreInfo() []*SstoreInfo { return bc.sstoreInfo }
func (bc *BranchContext) GetBranchDirection() bool     { return bc.isTaken }
func (bc *BranchContext) GetJudgementDirection() bool  { return bc.direction }
func (bc *BranchContext) GetFilled() bool              { return bc.isFilled }

func (bc *BranchContext) addBranchInfo(contractAddr common.Address, sUnit, cUnit TracingUnit, isVar, direction bool, id, judgement string) {
	bc.branchID = id
	bc.contractAddr = contractAddr
	bc.sUnit = sUnit
	bc.cUnit = cUnit
	bc.isVar = isVar
	bc.direction = direction
	bc.judgement = judgement
	bc.isFilled = true
}

func (bc *BranchContext) DecideDirection(res int) {
	if res == 0 {
		bc.isTaken = false
	} else {
		bc.isTaken = true
	}
}

func (bc *BranchContext) updateSStoreInfo(contractAddr common.Address, updatedVal uint256.Int, loc, val TracingUnit, isCompact bool) {
	newInfo := newSstoreInfo(contractAddr, updatedVal, loc, val, isCompact)
	bc.sstoreInfo = append(bc.sstoreInfo, newInfo)
}

func (bc *BranchContext) updateSnapshot(stack *Stack, memory *Memory, pc, jumpPc uint64, contract *Contract, depth int) *Snapshot {
	newSnapshot := createSnapShot(stack.copy(), memory.Clone(), pc, jumpPc, contract.Copy(), depth)
	bc.snapshots = append(bc.snapshots, newSnapshot)
	return newSnapshot
}

// Snapshot creates a tmp snapshot of current execution stack and relevant information
type Snapshot struct {
	curStack  *Stack
	curMemory *Memory
	pc        uint64
	jumpPc    uint64 // for tracking jumpi pos value in 'eq' judgement
	contract  *Contract
	depth     int
}

func createSnapShot(stack *Stack, memory *Memory, pc, jumpPc uint64, contract *Contract, depth int) *Snapshot {
	return &Snapshot{
		curStack:  stack,
		curMemory: memory,
		pc:        pc,
		jumpPc:    jumpPc,
		contract:  contract,
		depth:     depth,
	}
}

func (sp *Snapshot) UpdatePC(pc uint64)     { sp.pc = pc }
func (sp *Snapshot) GetStack() *Stack       { return sp.curStack }
func (sp *Snapshot) GetMemory() *Memory     { return sp.curMemory }
func (sp *Snapshot) GetPC() uint64          { return sp.pc }
func (sp *Snapshot) GetJumpPC() uint64      { return sp.jumpPc }
func (sp *Snapshot) GetContract() *Contract { return sp.contract }
func (sp *Snapshot) GetDepth() int          { return sp.depth }

// SstoreInfo records the updated variable under the current snapshot
type SstoreInfo struct {
	callerAddr common.Address
	updatedVal uint256.Int
	loc        TracingUnit
	val        TracingUnit
	compact    bool
}

func (ss *SstoreInfo) GetCallerAddr() common.Address { return ss.callerAddr }
func (ss *SstoreInfo) GetUpdatedValue() uint256.Int  { return ss.updatedVal }
func (ss *SstoreInfo) GetLocUnit() TracingUnit       { return ss.loc }
func (ss *SstoreInfo) GetValUnit() TracingUnit       { return ss.val }
func (ss *SstoreInfo) GetCompact() bool              { return ss.compact }

func newSstoreInfo(callerAddr common.Address, updatedVal uint256.Int, loc, val TracingUnit, isCompact bool) *SstoreInfo {
	return &SstoreInfo{
		callerAddr: callerAddr,
		updatedVal: updatedVal,
		loc:        loc,
		val:        val,
		compact:    isCompact,
	}
}
