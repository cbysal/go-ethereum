package vm

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

// TracingUnit defines an interface for three types of tracing units on the stack
type TracingUnit interface {
	GetValue() uint256.Int
	GetOffset() uint256.Int
	SetValue(value uint256.Int)
	Copy() TracingUnit
}

const (
	INPUT = iota
	STATE
)

// NormalUnit defines a non-tracing unit on the stack
type NormalUnit struct {
	curVal   uint256.Int
	isOffset bool
	offset   uint256.Int // record the offset of the state variable
	bits     int         // record the bits of the state variable
}

func NewNormalUnit(val uint256.Int) *NormalUnit {
	var offset uint256.Int
	return &NormalUnit{
		curVal:   val,
		isOffset: false,
		offset:   *offset.SetUint64(0),
		bits:     256,
	}
}

func (nu *NormalUnit) GetValue() uint256.Int  { return nu.curVal }
func (nu *NormalUnit) GetFlag() bool          { return nu.isOffset }
func (nu *NormalUnit) GetOffset() uint256.Int { return nu.offset }
func (nu *NormalUnit) GetBits() int           { return nu.bits }

func (nu *NormalUnit) SetValue(value uint256.Int) {
	nu.curVal = value
}

func (nu *NormalUnit) SetFlag() {
	nu.isOffset = true
}

func (nu *NormalUnit) SetOffset(offset uint256.Int) {
	nu.offset = offset
}

func (nu *NormalUnit) SetBits(bits int) {
	nu.bits = bits
}

func (nu *NormalUnit) Copy() TracingUnit {
	return &NormalUnit{
		curVal:   nu.curVal,
		isOffset: nu.isOffset,
		offset:   nu.offset,
		bits:     nu.bits,
	}
}

// CallDataUnit defines a unit representing the input value on the stack
type CallDataUnit struct {
	curVal uint256.Int
	offset uint256.Int
}

func NewCallDataUnit(val, offset uint256.Int) *CallDataUnit {
	return &CallDataUnit{
		curVal: val,
		offset: offset,
	}
}

func (cu *CallDataUnit) GetValue() uint256.Int  { return cu.curVal }
func (cu *CallDataUnit) GetOffset() uint256.Int { return cu.offset }

func (cu *CallDataUnit) SetValue(value uint256.Int) {
	cu.curVal = value
}

func (cu *CallDataUnit) Copy() TracingUnit {
	return &CallDataUnit{
		curVal: cu.curVal,
		offset: cu.offset,
	}
}

// StateUnit defines a unit representing the value of a state variable on the stack
type StateUnit struct {
	slot           uint256.Int
	storageVal     uint256.Int
	offset         uint256.Int
	curVal         uint256.Int
	blockNum       uint256.Int
	bits           int
	opTracer       []*opcodesTracer
	blockEnv       string // indicates if the variable is related to the current block environment
	signExtend     bool
	getBalanceAddr common.Address // record the queried (balance) contract address in a branch condition
}

type opcodesTracer struct {
	op        OpCode
	val       uint256.Int
	direction bool
	attaching TracingUnit
	// If the unit with which the computation is performed is also a StateUnit,
	// information about this unit needs to be maintained
}

func (tr *opcodesTracer) GetVal() uint256.Int       { return tr.val }
func (tr *opcodesTracer) GetOps() OpCode            { return tr.op }
func (tr *opcodesTracer) GetDirection() bool        { return tr.direction }
func (tr *opcodesTracer) GetAttaching() TracingUnit { return tr.attaching }

func newOpcodesTracer(op OpCode, val uint256.Int, direction bool, unit TracingUnit) *opcodesTracer {
	return &opcodesTracer{
		op:        op,
		val:       val,
		direction: direction,
		attaching: unit,
	}
}

func NewStateUnit(slot, storageVal, blockNum uint256.Int, blockEnv string, balAddr common.Address) *StateUnit {
	var offset uint256.Int
	return &StateUnit{
		slot:           slot,
		storageVal:     storageVal,
		offset:         *offset.SetUint64(0),
		curVal:         storageVal,
		blockNum:       blockNum,
		bits:           256,
		opTracer:       make([]*opcodesTracer, 0, 100),
		blockEnv:       blockEnv,
		signExtend:     false,
		getBalanceAddr: balAddr,
	}
}

func (su *StateUnit) GetSlot() uint256.Int         { return su.slot }
func (su *StateUnit) GetValue() uint256.Int        { return su.curVal }
func (su *StateUnit) GetStorageValue() uint256.Int { return su.storageVal }
func (su *StateUnit) GetBlockNum() uint256.Int     { return su.blockNum }
func (su *StateUnit) GetBits() int                 { return su.bits }
func (su *StateUnit) GetOffset() uint256.Int       { return su.offset }
func (su *StateUnit) GetTracer() []*opcodesTracer  { return su.opTracer }
func (su *StateUnit) GetBlockEnv() string          { return su.blockEnv }
func (su *StateUnit) GetSignExtend() bool          { return su.signExtend }
func (su *StateUnit) GetBalAddr() common.Address   { return su.getBalanceAddr }

func (su *StateUnit) SetValue(value uint256.Int) {
	su.curVal = value
}

// UpdateStorageValue only uses in the case that storage compact has happened
func (su *StateUnit) UpdateStorageValue(value uint256.Int) {
	su.storageVal = value
}

func (su *StateUnit) SetBits(bits int) {
	su.bits = bits
}

func (su *StateUnit) SetOffset(offset uint256.Int) {
	su.offset = offset
}

func (su *StateUnit) SetSignExtend() {
	su.signExtend = true
}

// Compare checks if the current value is equal to the storage value
func (su *StateUnit) Compare() bool {
	return su.storageVal == su.curVal
}

func (su *StateUnit) Record(op OpCode, val uint256.Int, direction bool, attaching TracingUnit) {
	newTracer := newOpcodesTracer(op, val, direction, attaching)
	su.opTracer = append(su.opTracer, newTracer)
}

func (su *StateUnit) DeleteLastOp() {
	if len(su.opTracer) > 0 {
		su.opTracer = su.opTracer[:len(su.opTracer)-1]
	}
}

func (su *StateUnit) ClearTracer() {
	su.opTracer = make([]*opcodesTracer, 0, 100)
}

func (su *StateUnit) Copy() TracingUnit {
	copiedSUnit := &StateUnit{
		slot:           su.slot,
		storageVal:     su.storageVal,
		bits:           su.bits,
		offset:         su.offset,
		curVal:         su.curVal,
		blockNum:       su.blockNum,
		opTracer:       make([]*opcodesTracer, len(su.opTracer)),
		blockEnv:       su.blockEnv,
		signExtend:     su.signExtend,
		getBalanceAddr: su.getBalanceAddr,
	}
	copy(copiedSUnit.opTracer, su.opTracer)
	return copiedSUnit
}
