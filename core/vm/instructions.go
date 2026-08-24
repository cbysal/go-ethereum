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

package vm

import (
	"errors"
	"math"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

func opAdd(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	x, xUnit := scope.Stack.pop()
	y, yUnit := scope.Stack.peek()
	originVal := *y
	y.Add(&x, y)
	if evm.Config.IsPreExec {
		merge(x, originVal, *y, ADD, xUnit, yUnit, scope, true)
	}
	return nil, nil
}

func opSub(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	x, xUnit := scope.Stack.pop()
	y, yUnit := scope.Stack.peek()
	originVal := *y
	y.Sub(&x, y)
	if evm.Config.IsPreExec {
		merge(x, originVal, *y, SUB, xUnit, yUnit, scope, true)
	}
	return nil, nil
}

func opMul(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	x, xUnit := scope.Stack.pop()
	y, yUnit := scope.Stack.peek()
	originVal := *y
	y.Mul(&x, y)
	if evm.Config.IsPreExec {
		normalUnit, ok := yUnit.(*NormalUnit)
		isOffset := ok && normalUnit.GetFlag()
		if isMask(x) && isOffset {
			bits := 4 * (len(x.Hex()) - 2)
			normalUnit.SetBits(bits)
		}
		merge(x, originVal, *y, MUL, xUnit, yUnit, scope, true)
	}
	return nil, nil
}

func opDiv(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	x, xUnit := scope.Stack.pop()
	y, yUnit := scope.Stack.peek()
	originVal := *y
	y.Div(&x, y)
	if evm.Config.IsPreExec {
		merge(x, originVal, *y, DIV, xUnit, yUnit, scope, true)
	}
	return nil, nil
}

func opSdiv(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	x, xUnit := scope.Stack.pop()
	y, yUnit := scope.Stack.peek()
	originVal := *y
	y.SDiv(&x, y)
	if evm.Config.IsPreExec {
		merge(x, originVal, *y, SDIV, xUnit, yUnit, scope, true)
	}
	return nil, nil
}

func opMod(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	x, xUnit := scope.Stack.pop()
	y, yUnit := scope.Stack.peek()
	originVal := *y
	y.Mod(&x, y)
	if evm.Config.IsPreExec {
		merge(x, originVal, *y, MOD, xUnit, yUnit, scope, true)
	}
	return nil, nil
}

func opSmod(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	x, xUnit := scope.Stack.pop()
	y, yUnit := scope.Stack.peek()
	originVal := *y
	y.SMod(&x, y)
	if evm.Config.IsPreExec {
		merge(x, originVal, *y, SMOD, xUnit, yUnit, scope, true)
	}
	return nil, nil
}

func opExp(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	base, baseUnit := scope.Stack.pop()
	exponent, exponentUnit := scope.Stack.peek()
	originVal := *exponent
	exponent.Exp(&base, exponent)
	if evm.Config.IsPreExec {
		merge(base, originVal, *exponent, EXP, baseUnit, exponentUnit, scope, true)
		if offsetUnit, ok := exponentUnit.(*NormalUnit); ok {
			offsetUnit.SetFlag()
			offsetUnit.SetOffset(*exponent)
		}
	}
	return nil, nil
}

func opSignExtend(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	back, _ := scope.Stack.pop()
	num, numUnit := scope.Stack.peek()
	num.ExtendSign(num, &back)
	if evm.Config.IsPreExec {
		if sUnit, ok := numUnit.(*StateUnit); ok {
			ops := sUnit.GetTracer()
			if len(ops) > 0 {
				latestTr := ops[len(ops)-1]
				if latestTr.GetOps() == DIV && !sUnit.GetSignExtend() {
					offset := latestTr.GetVal()
					bits := 8 * (int(back.Uint64()) + 1)
					sUnit.SetOffset(offset)
					sUnit.SetBits(bits)
					sUnit.UpdateStorageValue(*num)
					sUnit.ClearTracer()
					fragment, _ := scope.TmpState.GetFragment(scope.Contract.Address(), sUnit.GetSlot())
					fragment.GenerateVar(sUnit)
				}
			}
			sUnit.SetSignExtend()
		}
		numUnit.SetValue(*num)
	}
	return nil, nil
}

func opNot(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	x, xUnit := scope.Stack.peek()
	x.Not(x)
	if evm.Config.IsPreExec {
		xUnit.SetValue(*x)
		if unit, ok := xUnit.(*StateUnit); ok {
			unit.Record(NOT, uint256.Int{}, true, nil)
		}
	}
	return nil, nil
}

func opLt(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	x, xUnit := scope.Stack.pop()
	y, yUnit := scope.Stack.peek()
	if !evm.Config.IsPreExec {
		if x.Lt(y) {
			y.SetOne()
		} else {
			y.Clear()
		}
	} else {
		xVal, yVal, res, err := branchRecord(*pc, 0, evm, scope, xUnit, yUnit, "LT")
		if err != nil {
			return nil, err
		}
		txID := evm.TxContext.ID
		ret, _ := evm.Config.PreExecutionTable.GetResult(txID)
		switch res {
		case taken:
			y.SetOne()
			ret.UpdateDirection(1)
		case notTaken:
			y.Clear()
			ret.UpdateDirection(0)
		case uncertain:
			if xVal.Lt(&yVal) {
				y.SetOne()
			} else {
				y.Clear()
			}
			ret.UpdateDirection(int(y.Uint64()))
		case skip:
			if x.Lt(y) {
				y.SetOne()
			} else {
				y.Clear()
			}
		}
		merge(x, uint256.Int{}, *y, LT, xUnit, yUnit, scope, false)
	}
	return nil, nil
}

func opGt(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	x, xUnit := scope.Stack.pop()
	y, yUnit := scope.Stack.peek()
	if !evm.Config.IsPreExec {
		if x.Gt(y) {
			y.SetOne()
		} else {
			y.Clear()
		}
	} else {
		xVal, yVal, res, err := branchRecord(*pc, 0, evm, scope, xUnit, yUnit, "GT")
		if err != nil {
			return nil, err
		}
		txID := evm.TxContext.ID
		ret, _ := evm.Config.PreExecutionTable.GetResult(txID)
		switch res {
		case taken:
			y.SetOne()
			ret.UpdateDirection(1)
		case notTaken:
			y.Clear()
			ret.UpdateDirection(0)
		case uncertain:
			if xVal.Gt(&yVal) {
				y.SetOne()
			} else {
				y.Clear()
			}
			ret.UpdateDirection(int(y.Uint64()))
		case skip:
			if x.Gt(y) {
				y.SetOne()
			} else {
				y.Clear()
			}
		}
		merge(x, uint256.Int{}, *y, GT, xUnit, yUnit, scope, false)
	}
	return nil, nil
}

func opSlt(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	x, xUnit := scope.Stack.pop()
	y, yUnit := scope.Stack.peek()
	if !evm.Config.IsPreExec {
		if x.Slt(y) {
			y.SetOne()
		} else {
			y.Clear()
		}
	} else {
		xVal, yVal, res, err := branchRecord(*pc, 0, evm, scope, xUnit, yUnit, "SLT")
		if err != nil {
			return nil, err
		}
		txID := evm.TxContext.ID
		ret, _ := evm.Config.PreExecutionTable.GetResult(txID)
		switch res {
		case taken:
			y.SetOne()
			ret.UpdateDirection(1)
		case notTaken:
			y.Clear()
			ret.UpdateDirection(0)
		case uncertain:
			if xVal.Slt(&yVal) {
				y.SetOne()
			} else {
				y.Clear()
			}
			ret.UpdateDirection(int(y.Uint64()))
		case skip:
			if x.Slt(y) {
				y.SetOne()
			} else {
				y.Clear()
			}
		}
		merge(x, uint256.Int{}, *y, SLT, xUnit, yUnit, scope, false)
	}
	return nil, nil
}

func opSgt(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	x, xUnit := scope.Stack.pop()
	y, yUnit := scope.Stack.peek()
	if !evm.Config.IsPreExec {
		if x.Sgt(y) {
			y.SetOne()
		} else {
			y.Clear()
		}
	} else {
		xVal, yVal, res, err := branchRecord(*pc, 0, evm, scope, xUnit, yUnit, "SGT")
		if err != nil {
			return nil, err
		}
		txID := evm.TxContext.ID
		ret, _ := evm.Config.PreExecutionTable.GetResult(txID)
		switch res {
		case taken:
			y.SetOne()
			ret.UpdateDirection(1)
		case notTaken:
			y.Clear()
			ret.UpdateDirection(0)
		case uncertain:
			if xVal.Sgt(&yVal) {
				y.SetOne()
			} else {
				y.Clear()
			}
			ret.UpdateDirection(int(y.Uint64()))
		case skip:
			if x.Sgt(y) {
				y.SetOne()
			} else {
				y.Clear()
			}
		}
		merge(x, uint256.Int{}, *y, SGT, xUnit, yUnit, scope, false)
	}
	return nil, nil
}

func opEq(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	x, xUnit := scope.Stack.pop()
	y, yUnit := scope.Stack.peek()
	originVal := *y
	if x.Eq(y) {
		y.SetOne()
	} else {
		y.Clear()
	}
	if evm.Config.IsPreExec {
		merge(x, originVal, *y, EQ, xUnit, yUnit, scope, false)
	}
	return nil, nil
}

func opIszero(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	x, xUnit := scope.Stack.peek()
	if x.IsZero() {
		x.SetOne()
	} else {
		x.Clear()
	}
	if evm.Config.IsPreExec {
		xUnit.SetValue(*x)
	}
	return nil, nil
}

func opAnd(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	x, xUnit := scope.Stack.pop()
	y, yUnit := scope.Stack.peek()
	originVal := *y
	y.And(&x, y)
	if evm.Config.IsPreExec {
		normalUnit, ok := xUnit.(*NormalUnit)
		unit, ok2 := yUnit.(*StateUnit)
		if ok && ok2 && isMask(x) {
			if len(x.Hex()) < 66 {
				ops := unit.GetTracer()
				if len(ops) > 0 {
					latestTr := ops[len(ops)-1]
					if latestTr.GetOps() == DIV {
						offset := latestTr.val
						bits := 4 * (len(x.Hex()) - 2)
						unit.SetOffset(offset)
						unit.SetBits(bits)
						unit.UpdateStorageValue(*y)
						unit.ClearTracer()
						fragment, exist := scope.TmpState.GetFragment(scope.Contract.Address(), unit.GetSlot())
						if !exist {
							fragment = scope.TmpState.InsertUnit(scope.Contract.Address(), unit.GetSlot())
						}
						fragment.GenerateVar(unit)
						unit.SetValue(*y)
						return nil, nil
					}
				}
			} else {
				bits := normalUnit.GetBits()
				offset := normalUnit.GetOffset()
				if offset.Uint64() > 0 && bits < 256 {
					unit.SetBits(bits)
					unit.SetOffset(offset)
					fragment, exist := scope.TmpState.GetFragment(scope.Contract.Address(), unit.GetSlot())
					if !exist {
						fragment = scope.TmpState.InsertUnit(scope.Contract.Address(), unit.GetSlot())
					}
					fragment.GenerateVar(unit)
					unit.SetValue(*y)
					return nil, nil
				}
			}
		}
		merge(x, originVal, *y, AND, xUnit, yUnit, scope, true)
	}
	return nil, nil
}

func opOr(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	x, xUnit := scope.Stack.pop()
	y, yUnit := scope.Stack.peek()
	originVal := *y
	y.Or(&x, y)
	if evm.Config.IsPreExec {
		merge(x, originVal, *y, OR, xUnit, yUnit, scope, true)
	}
	return nil, nil
}

func opXor(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	x, xUnit := scope.Stack.pop()
	y, yUnit := scope.Stack.peek()
	originVal := *y
	y.Xor(&x, y)
	if evm.Config.IsPreExec {
		merge(x, originVal, *y, XOR, xUnit, yUnit, scope, true)
	}
	return nil, nil
}

func opByte(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	th, thUnit := scope.Stack.pop()
	val, valUnit := scope.Stack.peek()
	originVal := *val
	val.Byte(&th)
	if evm.Config.IsPreExec {
		merge(th, originVal, *val, BYTE, thUnit, valUnit, scope, true)
	}
	return nil, nil
}

func opAddmod(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	x, xUnit := scope.Stack.pop()
	if !evm.Config.IsPreExec {
		y, _ := scope.Stack.pop()
		z, _ := scope.Stack.peek()
		z.AddMod(&x, &y, z)
	} else {
		y, yUnit := scope.Stack.peek()
		originVal := *y
		y.Add(&x, y)
		merge(x, originVal, *y, ADD, xUnit, yUnit, scope, true)

		y2, yUnit2 := scope.Stack.pop()
		z, zUnit := scope.Stack.peek()
		originVal2 := *z
		if z.IsZero() {
			z.Clear()
		} else {
			z.Mod(&y2, z)
		}
		merge(y2, originVal2, *z, MOD, yUnit2, zUnit, scope, true)
	}
	return nil, nil
}

func opMulmod(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	x, xUnit := scope.Stack.pop()
	if !evm.Config.IsPreExec {
		y, _ := scope.Stack.pop()
		z, _ := scope.Stack.peek()
		z.MulMod(&x, &y, z)
	} else {
		y, yUnit := scope.Stack.peek()
		originVal := *y
		y.Mul(&x, y)
		merge(x, originVal, *y, MUL, xUnit, yUnit, scope, true)

		y2, yUnit2 := scope.Stack.pop()
		z, zUnit := scope.Stack.peek()
		originVal2 := *z
		z.Mod(&y2, z)
		merge(y2, originVal2, *z, MOD, yUnit2, zUnit, scope, true)
	}
	return nil, nil
}

// opSHL implements Shift Left
// The SHL instruction (shift left) pops 2 values from the stack, first arg1 and then arg2,
// and pushes on the stack arg2 shifted to the left by arg1 number of bits.
func opSHL(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	// Note, second operand is left in the stack; accumulate result into it, and no need to push it afterwards
	shift, shiftUnit := scope.Stack.pop()
	value, valueUnit := scope.Stack.peek()
	originVal := *value
	if shift.LtUint64(256) {
		value.Lsh(value, uint(shift.Uint64()))
	} else {
		value.Clear()
	}
	if evm.Config.IsPreExec {
		merge(shift, originVal, *value, SHL, shiftUnit, valueUnit, scope, true)
	}
	return nil, nil
}

// opSHR implements Logical Shift Right
// The SHR instruction (logical shift right) pops 2 values from the stack, first arg1 and then arg2,
// and pushes on the stack arg2 shifted to the right by arg1 number of bits with zero fill.
func opSHR(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	// Note, second operand is left in the stack; accumulate result into it, and no need to push it afterwards
	shift, shiftUnit := scope.Stack.pop()
	value, valueUnit := scope.Stack.peek()
	originVal := *value
	if shift.LtUint64(256) {
		value.Rsh(value, uint(shift.Uint64()))
	} else {
		value.Clear()
	}
	if evm.Config.IsPreExec {
		merge(shift, originVal, *value, SHR, shiftUnit, valueUnit, scope, true)
	}
	return nil, nil
}

// opSAR implements Arithmetic Shift Right
// The SAR instruction (arithmetic shift right) pops 2 values from the stack, first arg1 and then arg2,
// and pushes on the stack arg2 shifted to the right by arg1 number of bits with sign extension.
func opSAR(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	shift, shiftUnit := scope.Stack.pop()
	value, valueUnit := scope.Stack.peek()
	originVal := *value
	if shift.GtUint64(256) {
		if value.Sign() >= 0 {
			value.Clear()
		} else {
			// Max negative shift: all bits set
			value.SetAllOne()
		}
		if evm.Config.IsPreExec {
			merge(shift, originVal, *value, SAR, shiftUnit, valueUnit, scope, true)
		}
		return nil, nil
	}
	n := uint(shift.Uint64())
	value.SRsh(value, n)
	if evm.Config.IsPreExec {
		merge(shift, originVal, *value, SAR, shiftUnit, valueUnit, scope, true)
	}
	return nil, nil
}

func opKeccak256(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	offset, offsetUnit := scope.Stack.pop()
	size, sizeUnit := scope.Stack.peek()
	originVal := *size
	data := scope.Memory.GetPtr(offset.Uint64(), size.Uint64())

	evm.hasher.Reset()
	evm.hasher.Write(data)
	evm.hasher.Read(evm.hasherBuf[:])

	if evm.Config.EnablePreimageRecording {
		evm.StateDB.AddPreimage(evm.hasherBuf, data)
	}
	size.SetBytes(evm.hasherBuf[:])
	if evm.Keccak256Hashes != nil && len(data) == 2*common.HashLength {
		hashes := make([]common.Hash, 0, len(data)/common.HashLength)
		for i := 0; i < len(data); i += common.HashLength {
			hashes = append(hashes, common.BytesToHash(data[i:i+common.HashLength]))
		}
		evm.Keccak256Hashes[evm.hasherBuf] = hashes
	}
	if evm.Config.IsPreExec {
		merge(offset, originVal, *size, KECCAK256, offsetUnit, sizeUnit, scope, false)
	}
	return nil, nil
}

func opAddress(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.push(new(uint256.Int).SetBytes(scope.Contract.Address().Bytes()))
	return nil, nil
}

func opBalance(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	slot, _ := scope.Stack.peek()
	address := common.Address(slot.Bytes20())
	slot.Set(evm.StateDB.GetBalance(address))
	if evm.Config.IsPreExec {
		scope.Stack.updateUnit(STATE, uint256.Int{}, uint256.Int{}, *slot, uint256.Int{}, "BALANCE", address)
		txID := evm.TxContext.ID
		ret, _ := evm.Config.PreExecutionTable.GetResult(txID)
		ret.CacheReadSet()
	}
	return nil, nil
}

func opOrigin(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.push(new(uint256.Int).SetBytes(evm.Origin.Bytes()))
	return nil, nil
}

func opCaller(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.push(new(uint256.Int).SetBytes(scope.Contract.Caller().Bytes()))
	return nil, nil
}

func opCallValue(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.push(scope.Contract.value)
	return nil, nil
}

func opCallDataLoad(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	x, _ := scope.Stack.peek()
	if offset, overflow := x.Uint64WithOverflow(); !overflow {
		data := getData(scope.Contract.Input, offset, 32)
		x.SetBytes(data)
		if evm.Config.IsPreExec {
			sig := x.Hex()[:]
			scope.Signature = sig
			scope.Stack.updateUnit(INPUT, uint256.Int{}, *uint256.NewInt(offset), *x, uint256.Int{}, "nil", common.Address{})
		}
	} else {
		x.Clear()
		if evm.Config.IsPreExec {
			scope.Stack.updateUnit(INPUT, uint256.Int{}, uint256.Int{}, *x, uint256.Int{}, "nil", common.Address{})
		}
	}
	return nil, nil
}

func opCallDataSize(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.push(new(uint256.Int).SetUint64(uint64(len(scope.Contract.Input))))
	return nil, nil
}

func opCallDataCopy(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	var (
		memOffset, _  = scope.Stack.pop()
		dataOffset, _ = scope.Stack.pop()
		length, _     = scope.Stack.pop()
	)
	dataOffset64, overflow := dataOffset.Uint64WithOverflow()
	if overflow {
		dataOffset64 = math.MaxUint64
	}
	// These values are checked for overflow during gas cost calculation
	memOffset64 := memOffset.Uint64()
	length64 := length.Uint64()
	scope.Memory.Set(memOffset64, length64, getData(scope.Contract.Input, dataOffset64, length64))

	return nil, nil
}

func opReturnDataSize(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.push(new(uint256.Int).SetUint64(uint64(len(evm.returnData))))
	return nil, nil
}

func opReturnDataCopy(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	var (
		memOffset, _  = scope.Stack.pop()
		dataOffset, _ = scope.Stack.pop()
		length, _     = scope.Stack.pop()
	)

	offset64, overflow := dataOffset.Uint64WithOverflow()
	if overflow {
		return nil, ErrReturnDataOutOfBounds
	}
	// we can reuse dataOffset now (aliasing it for clarity)
	var end = dataOffset
	end.Add(&dataOffset, &length)
	end64, overflow := end.Uint64WithOverflow()
	if overflow || uint64(len(evm.returnData)) < end64 {
		return nil, ErrReturnDataOutOfBounds
	}
	scope.Memory.Set(memOffset.Uint64(), length.Uint64(), evm.returnData[offset64:end64])
	return nil, nil
}

func opExtCodeSize(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	slot, slotUnit := scope.Stack.peek()
	slot.SetUint64(uint64(evm.StateDB.GetCodeSize(slot.Bytes20())))
	if evm.Config.IsPreExec {
		slotUnit.SetValue(*slot)
		txID := evm.TxContext.ID
		ret, _ := evm.Config.PreExecutionTable.GetResult(txID)
		ret.CacheReadSet()
	}
	return nil, nil
}

func opCodeSize(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.push(new(uint256.Int).SetUint64(uint64(len(scope.Contract.Code))))
	return nil, nil
}

func opCodeCopy(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	var (
		memOffset, _  = scope.Stack.pop()
		codeOffset, _ = scope.Stack.pop()
		length, _     = scope.Stack.pop()
	)
	uint64CodeOffset, overflow := codeOffset.Uint64WithOverflow()
	if overflow {
		uint64CodeOffset = math.MaxUint64
	}

	codeCopy := getData(scope.Contract.Code, uint64CodeOffset, length.Uint64())
	scope.Memory.Set(memOffset.Uint64(), length.Uint64(), codeCopy)
	return nil, nil
}

func opExtCodeCopy(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	var (
		stack         = scope.Stack
		a, _          = stack.pop()
		memOffset, _  = stack.pop()
		codeOffset, _ = stack.pop()
		length, _     = stack.pop()
	)
	uint64CodeOffset, overflow := codeOffset.Uint64WithOverflow()
	if overflow {
		uint64CodeOffset = math.MaxUint64
	}
	addr := common.Address(a.Bytes20())
	code := evm.StateDB.GetCode(addr)
	codeCopy := getData(code, uint64CodeOffset, length.Uint64())
	scope.Memory.Set(memOffset.Uint64(), length.Uint64(), codeCopy)
	if evm.Config.IsPreExec {
		txID := evm.TxContext.ID
		ret, _ := evm.Config.PreExecutionTable.GetResult(txID)
		ret.CacheReadSet()
	}

	return nil, nil
}

// opExtCodeHash returns the code hash of a specified account.
// There are several cases when the function is called, while we can relay everything
// to `state.GetCodeHash` function to ensure the correctness.
//
//  1. Caller tries to get the code hash of a normal contract account, state
//     should return the relative code hash and set it as the result.
//
//  2. Caller tries to get the code hash of a non-existent account, state should
//     return common.Hash{} and zero will be set as the result.
//
//  3. Caller tries to get the code hash for an account without contract code, state
//     should return emptyCodeHash(0xc5d246...) as the result.
//
//  4. Caller tries to get the code hash of a precompiled account, the result should be
//     zero or emptyCodeHash.
//
// It is worth noting that in order to avoid unnecessary create and clean, all precompile
// accounts on mainnet have been transferred 1 wei, so the return here should be
// emptyCodeHash. If the precompile account is not transferred any amount on a private or
// customized chain, the return value will be zero.
//
//  5. Caller tries to get the code hash for an account which is marked as self-destructed
//     in the current transaction, the code hash of this account should be returned.
//
//  6. Caller tries to get the code hash for an account which is marked as deleted, this
//     account should be regarded as a non-existent account and zero should be returned.
func opExtCodeHash(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	slot, slotUnit := scope.Stack.peek()
	address := common.Address(slot.Bytes20())
	if evm.StateDB.Empty(address) {
		slot.Clear()
	} else {
		slot.SetBytes(evm.StateDB.GetCodeHash(address).Bytes())
	}
	if evm.Config.IsPreExec {
		slotUnit.SetValue(*slot)
		txID := evm.TxContext.ID
		ret, _ := evm.Config.PreExecutionTable.GetResult(txID)
		ret.CacheReadSet()
	}
	return nil, nil
}

func opGasprice(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	v, _ := uint256.FromBig(evm.GasPrice)
	scope.Stack.push(v)
	if evm.Config.IsPreExec {
		scope.Stack.updateUnit(STATE, uint256.Int{}, uint256.Int{}, *v, uint256.Int{}, "GASPRICE", common.Address{})
	}
	return nil, nil
}

func opBlockhash(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	num, numUnit := scope.Stack.peek()
	originNum := *num
	num64, overflow := num.Uint64WithOverflow()
	if overflow {
		num.Clear()
		return nil, nil
	}

	var upper, lower uint64
	upper = evm.Context.BlockNumber.Uint64()
	if upper < 257 {
		lower = 0
	} else {
		lower = upper - 256
	}
	if num64 >= lower && num64 < upper {
		res := evm.Context.GetHash(num64)
		if witness := evm.StateDB.Witness(); witness != nil {
			witness.AddBlockHash(num64)
		}
		if tracer := evm.Config.Tracer; tracer != nil && tracer.OnBlockHashRead != nil {
			tracer.OnBlockHashRead(num64, res)
		}
		num.SetBytes(res[:])
	} else {
		num.Clear()
	}
	if evm.Config.IsPreExec {
		numUnit.SetValue(*num)
		scope.Stack.updateUnit(STATE, uint256.Int{}, uint256.Int{}, *num, originNum, "BLOCKHASH", common.Address{})
	}
	return nil, nil
}

func opCoinbase(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	v := new(uint256.Int).SetBytes(evm.Context.Coinbase.Bytes())
	scope.Stack.push(v)
	if evm.Config.IsPreExec {
		scope.Stack.updateUnit(STATE, uint256.Int{}, uint256.Int{}, *v, uint256.Int{}, "COINBASE", common.Address{})
	}
	return nil, nil
}

func opTimestamp(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	v := new(uint256.Int).SetUint64(evm.Context.Time)
	scope.Stack.push(v)
	if evm.Config.IsPreExec {
		scope.Stack.updateUnit(STATE, uint256.Int{}, uint256.Int{}, *v, uint256.Int{}, "TIMESTAMP", common.Address{})
	}
	return nil, nil
}

func opNumber(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	v, _ := uint256.FromBig(evm.Context.BlockNumber)
	scope.Stack.push(v)
	if evm.Config.IsPreExec {
		scope.Stack.updateUnit(STATE, uint256.Int{}, uint256.Int{}, *v, uint256.Int{}, "NUMBER", common.Address{})
	}
	return nil, nil
}

func opDifficulty(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	v, _ := uint256.FromBig(evm.Context.Difficulty)
	scope.Stack.push(v)
	if evm.Config.IsPreExec {
		scope.Stack.updateUnit(STATE, uint256.Int{}, uint256.Int{}, *v, uint256.Int{}, "DIFFICULTY", common.Address{})
	}
	return nil, nil
}

func opRandom(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	v := new(uint256.Int).SetBytes(evm.Context.Random.Bytes())
	scope.Stack.push(v)
	if evm.Config.IsPreExec {
		scope.Stack.updateUnit(STATE, uint256.Int{}, uint256.Int{}, *v, uint256.Int{}, "RANDOM", common.Address{})
	}
	return nil, nil
}

func opGasLimit(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	v := new(uint256.Int).SetUint64(evm.Context.GasLimit)
	scope.Stack.push(v)
	if evm.Config.IsPreExec {
		scope.Stack.updateUnit(STATE, uint256.Int{}, uint256.Int{}, *v, uint256.Int{}, "GASLIMIT", common.Address{})
	}
	return nil, nil
}

func opPop(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.pop()
	return nil, nil
}

func opMload(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	v, unit := scope.Stack.peek()
	offset := v.Uint64()
	v.SetBytes(scope.Memory.GetPtr(offset, 32))
	if evm.Config.IsPreExec {
		unit.SetValue(*v)
	}
	return nil, nil
}

func opMstore(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	mStart, _ := scope.Stack.pop()
	val, _ := scope.Stack.pop()
	scope.Memory.Set32(mStart.Uint64(), &val)
	return nil, nil
}

func opMstore8(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	off, _ := scope.Stack.pop()
	val, _ := scope.Stack.pop()
	scope.Memory.store[off.Uint64()] = byte(val.Uint64())
	return nil, nil
}

func opSload(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	loc, _ := scope.Stack.peek()
	tmpLoc := *loc
	hash := common.Hash(loc.Bytes32())
	val := evm.StateDB.GetState(scope.Contract.Address(), hash)
	loc.SetBytes(val.Bytes())
	if evm.Config.IsPreExec {
		scope.Stack.updateUnit(STATE, tmpLoc, uint256.Int{}, *loc, uint256.Int{}, "nil", common.Address{})
		scope.TmpState.InsertUnit(scope.Contract.Address(), tmpLoc)
		txID := evm.TxContext.ID
		ret, _ := evm.Config.PreExecutionTable.GetResult(txID)
		ret.CacheReadSet()
	}
	return nil, nil
}

func opSstore(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	if evm.readOnly {
		return nil, ErrWriteProtection
	}
	loc, locUnit := scope.Stack.pop()
	val, valUnit := scope.Stack.pop()
	if !evm.Config.IsPreExec {
		evm.StateDB.SetState(scope.Contract.Address(), loc.Bytes32(), val.Bytes32())
	} else {
		var (
			updatedVal uint256.Int
			offset     uint256.Int
			isCompact  bool
			signExtend bool
			err        error
		)
		txID := evm.TxContext.ID
		ret, _ := evm.Config.PreExecutionTable.GetResult(txID)
		if fragment, ok := scope.TmpState.GetFragment(scope.Contract.Address(), loc); ok {
			stateUnit, ok2 := valUnit.(*StateUnit)
			if ok2 && fragment.GetCompact() {
				offset = stateUnit.GetOffset()
				bits := stateUnit.GetBits()
				ops := stateUnit.GetTracer()
				if offset.Uint64() > 0 && bits < 256 {
					isCompact = true
					if len(ops) > 0 {
						attaching := ops[len(ops)-1].GetAttaching()
						if opUnit, ok3 := attaching.(*StateUnit); ok3 {
							signExtend = opUnit.GetSignExtend()
							if signExtend {
								stateUnit.SetSignExtend()
							}
						}
					}
					updatedVal = fetchStorageVal(val, offset, bits, signExtend)
				} else {
					isCompact = false
					updatedVal = val
				}
			} else {
				isCompact = false
				updatedVal = val
			}
		} else {
			isCompact = false
			updatedVal = val
			scope.TmpState.Space[common.AddrU256{Addr: scope.Contract.Address(), U256: loc}] = NewFragment()
		}
		ret.CacheSStoreInfo(scope.Contract.Address(), updatedVal, locUnit.Copy(), valUnit.Copy(), isCompact)

		if evm.Config.VarTable.VarExist(scope.Contract.Address(), loc.Bytes32(), offset) {
			tip := evm.TxContext.GasTip
			if isCompact {
				_, err = evm.Config.MVCache.SetCompactedStorageForWrite(scope.Contract.Address(), loc,
					updatedVal, offset, txID, tip)
			} else {
				_, err = evm.Config.MVCache.SetStorageForWrite(scope.Contract.Address(), loc,
					updatedVal, txID, tip)
			}
			if err != nil {
				return nil, err
			}
		}
		ret.CacheWriteSet()
	}
	return nil, nil
}

func opJump(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	if evm.abort.Load() {
		return nil, errStopToken
	}
	pos, _ := scope.Stack.pop()
	if !scope.Contract.validJumpdest(&pos) {
		return nil, ErrInvalidJump
	}
	*pc = pos.Uint64() - 1 // pc will be increased by the interpreter loop
	return nil, nil
}

func opJumpi(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	if evm.abort.Load() {
		return nil, errStopToken
	}
	pos, _ := scope.Stack.pop()
	cond, condUnit := scope.Stack.pop()
	if evm.Config.IsPreExec {
		if sUnit, ok := condUnit.(*StateUnit); ok {
			ops := sUnit.GetTracer()
			if len(ops) > 0 {
				latestTr := ops[len(ops)-1]
				if latestTr.GetOps() == SUB {
					opUnit := latestTr.GetAttaching()
					firstVal, secondVal, res, err := branchRecord(*pc, pos.Uint64(), evm, scope, sUnit, opUnit, "EQ")
					if err != nil {
						return nil, err
					}
					txID := evm.TxContext.ID
					ret, _ := evm.Config.PreExecutionTable.GetResult(txID)
					switch res {
					case taken:
						ret.UpdateDirection(1)
						return nil, nil
					case notTaken:
						if !scope.Contract.validJumpdest(&pos) {
							return nil, ErrInvalidJump
						}
						*pc = pos.Uint64() - 1 // pc will be increased by the evm loop
						ret.UpdateDirection(0)
						return nil, nil
					case uncertain:
						firstVal.Sub(&firstVal, &secondVal)
						if !firstVal.IsZero() {
							if !scope.Contract.validJumpdest(&pos) {
								return nil, ErrInvalidJump
							}
							*pc = pos.Uint64() - 1 // pc will be increased by the evm loop
							ret.UpdateDirection(0)
						} else {
							ret.UpdateDirection(1)
						}
						return nil, nil
					}
				}
			}
		}
	}
	if !cond.IsZero() {
		if !scope.Contract.validJumpdest(&pos) {
			return nil, ErrInvalidJump
		}
		*pc = pos.Uint64() - 1 // pc will be increased by the interpreter loop
	}
	return nil, nil
}

func opJumpdest(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	return nil, nil
}

func opPc(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.push(new(uint256.Int).SetUint64(*pc))
	return nil, nil
}

func opMsize(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.push(new(uint256.Int).SetUint64(uint64(scope.Memory.Len())))
	return nil, nil
}

func opGas(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.push(new(uint256.Int).SetUint64(scope.Contract.Gas))
	return nil, nil
}

func opSwap1(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.swap1()
	return nil, nil
}

func opSwap2(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.swap2()
	return nil, nil
}

func opSwap3(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.swap3()
	return nil, nil
}

func opSwap4(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.swap4()
	return nil, nil
}

func opSwap5(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.swap5()
	return nil, nil
}

func opSwap6(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.swap6()
	return nil, nil
}

func opSwap7(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.swap7()
	return nil, nil
}

func opSwap8(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.swap8()
	return nil, nil
}

func opSwap9(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.swap9()
	return nil, nil
}

func opSwap10(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.swap10()
	return nil, nil
}

func opSwap11(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.swap11()
	return nil, nil
}

func opSwap12(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.swap12()
	return nil, nil
}

func opSwap13(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.swap13()
	return nil, nil
}

func opSwap14(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.swap14()
	return nil, nil
}

func opSwap15(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.swap15()
	return nil, nil
}

func opSwap16(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	scope.Stack.swap16()
	return nil, nil
}

func opCreate(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	if evm.readOnly {
		return nil, ErrWriteProtection
	}
	var (
		value, _  = scope.Stack.pop()
		offset, _ = scope.Stack.pop()
		size, _   = scope.Stack.pop()
		input     = scope.Memory.GetCopy(offset.Uint64(), size.Uint64())
		gas       = scope.Contract.Gas
	)
	if evm.chainRules.IsEIP150 {
		gas -= gas / 64
	}

	// reuse size int for stackvalue
	stackvalue := size

	scope.Contract.UseGas(gas, evm.Config.Tracer, tracing.GasChangeCallContractCreation)

	res, addr, returnGas, suberr := evm.Create(scope.Contract.Address(), input, gas, &value)
	// Push item on the stack based on the returned error. If the ruleset is
	// homestead we must check for CodeStoreOutOfGasError (homestead only
	// rule) and treat as an error, if the ruleset is frontier we must
	// ignore this error and pretend the operation was successful.
	if evm.chainRules.IsHomestead && suberr == ErrCodeStoreOutOfGas {
		stackvalue.Clear()
	} else if suberr != nil && suberr != ErrCodeStoreOutOfGas {
		stackvalue.Clear()
	} else {
		stackvalue.SetBytes(addr.Bytes())
	}
	scope.Stack.push(&stackvalue)

	scope.Contract.RefundGas(returnGas, evm.Config.Tracer, tracing.GasChangeCallLeftOverRefunded)

	if suberr == ErrExecutionReverted {
		evm.returnData = res // set REVERT data to return data buffer
		return res, nil
	}
	evm.returnData = nil // clear dirty return data buffer
	return nil, nil
}

func opCreate2(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	if evm.readOnly {
		return nil, ErrWriteProtection
	}
	var (
		endowment, _ = scope.Stack.pop()
		offset, _    = scope.Stack.pop()
		size, _      = scope.Stack.pop()
		salt, _      = scope.Stack.pop()
		input        = scope.Memory.GetCopy(offset.Uint64(), size.Uint64())
		gas          = scope.Contract.Gas
	)

	// Apply EIP150
	gas -= gas / 64
	scope.Contract.UseGas(gas, evm.Config.Tracer, tracing.GasChangeCallContractCreation2)
	// reuse size int for stackvalue
	stackvalue := size
	res, addr, returnGas, suberr := evm.Create2(scope.Contract.Address(), input, gas,
		&endowment, &salt)
	// Push item on the stack based on the returned error.
	if suberr != nil {
		stackvalue.Clear()
	} else {
		stackvalue.SetBytes(addr.Bytes())
	}
	scope.Stack.push(&stackvalue)
	scope.Contract.RefundGas(returnGas, evm.Config.Tracer, tracing.GasChangeCallLeftOverRefunded)

	if suberr == ErrExecutionReverted {
		evm.returnData = res // set REVERT data to return data buffer
		return res, nil
	}
	evm.returnData = nil // clear dirty return data buffer
	return nil, nil
}

func opCall(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	stack := scope.Stack
	if evm.Config.IsPreExec {
		txID := evm.TxContext.ID
		res, _ := evm.Config.PreExecutionTable.GetResult(txID)
		res.CacheSnapshot(stack, scope.Memory, *pc, 0, scope.Contract, evm.depth)
	}
	// Pop gas. The actual gas in evm.callGasTemp.
	// We can use this as a temporary value
	temp, _ := stack.pop()
	gas := evm.callGasTemp
	// Pop other call parameters.
	addr, _ := stack.pop()
	value, _ := stack.pop()
	inOffset, _ := stack.pop()
	inSize, _ := stack.pop()
	retOffset, _ := stack.pop()
	retSize, _ := stack.pop()
	toAddr := common.Address(addr.Bytes20())
	// Get the arguments from the memory.
	args := scope.Memory.GetPtr(inOffset.Uint64(), inSize.Uint64())

	if evm.readOnly && !value.IsZero() {
		return nil, ErrWriteProtection
	}
	if !value.IsZero() {
		gas += params.CallStipend
	}
	ret, returnGas, err := evm.Call(scope.Contract.Address(), toAddr, args, gas, &value)

	if err != nil {
		temp.Clear()
	} else {
		temp.SetOne()
	}
	stack.push(&temp)
	if err == nil || err == ErrExecutionReverted {
		scope.Memory.Set(retOffset.Uint64(), retSize.Uint64(), ret)
	}

	scope.Contract.RefundGas(returnGas, evm.Config.Tracer, tracing.GasChangeCallLeftOverRefunded)

	evm.returnData = ret
	return ret, nil
}

func opCallCode(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	// Pop gas. The actual gas is in evm.callGasTemp.
	stack := scope.Stack
	if evm.Config.IsPreExec {
		txID := evm.TxContext.ID
		res, _ := evm.Config.PreExecutionTable.GetResult(txID)
		if evm.Config.IsPreExec {
			res.CacheSnapshot(stack, scope.Memory, *pc, 0, scope.Contract, evm.depth)
		}
	}
	// We use it as a temporary value
	temp, _ := stack.pop()
	gas := evm.callGasTemp
	// Pop other call parameters.
	addr, _ := stack.pop()
	value, _ := stack.pop()
	inOffset, _ := stack.pop()
	inSize, _ := stack.pop()
	retOffset, _ := stack.pop()
	retSize, _ := stack.pop()
	toAddr := common.Address(addr.Bytes20())
	// Get arguments from the memory.
	args := scope.Memory.GetPtr(inOffset.Uint64(), inSize.Uint64())

	if !value.IsZero() {
		gas += params.CallStipend
	}

	ret, returnGas, err := evm.CallCode(scope.Contract.Address(), toAddr, args, gas, &value)
	if err != nil {
		temp.Clear()
	} else {
		temp.SetOne()
	}
	stack.push(&temp)
	if err == nil || err == ErrExecutionReverted {
		scope.Memory.Set(retOffset.Uint64(), retSize.Uint64(), ret)
	}

	scope.Contract.RefundGas(returnGas, evm.Config.Tracer, tracing.GasChangeCallLeftOverRefunded)

	evm.returnData = ret
	return ret, nil
}

func opDelegateCall(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	stack := scope.Stack
	if evm.Config.IsPreExec {
		txID := evm.TxContext.ID
		res, _ := evm.Config.PreExecutionTable.GetResult(txID)
		if evm.Config.IsPreExec {
			res.CacheSnapshot(stack, scope.Memory, *pc, 0, scope.Contract, evm.depth)
		}
	}
	// Pop gas. The actual gas is in evm.callGasTemp.
	// We use it as a temporary value
	temp, _ := stack.pop()
	gas := evm.callGasTemp
	// Pop other call parameters.
	addr, _ := stack.pop()
	inOffset, _ := stack.pop()
	inSize, _ := stack.pop()
	retOffset, _ := stack.pop()
	retSize, _ := stack.pop()
	toAddr := common.Address(addr.Bytes20())
	// Get arguments from the memory.
	args := scope.Memory.GetPtr(inOffset.Uint64(), inSize.Uint64())

	ret, returnGas, err := evm.DelegateCall(scope.Contract.Caller(), scope.Contract.Address(), toAddr, args, gas, scope.Contract.value)
	if err != nil {
		temp.Clear()
	} else {
		temp.SetOne()
	}
	stack.push(&temp)
	if err == nil || err == ErrExecutionReverted {
		scope.Memory.Set(retOffset.Uint64(), retSize.Uint64(), ret)
	}

	scope.Contract.RefundGas(returnGas, evm.Config.Tracer, tracing.GasChangeCallLeftOverRefunded)

	evm.returnData = ret
	return ret, nil
}

func opStaticCall(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	// Pop gas. The actual gas is in evm.callGasTemp.
	stack := scope.Stack
	if evm.Config.IsPreExec {
		txID := evm.TxContext.ID
		res, _ := evm.Config.PreExecutionTable.GetResult(txID)
		if evm.Config.IsPreExec {
			res.CacheSnapshot(stack, scope.Memory, *pc, 0, scope.Contract, evm.depth)
		}
	}
	// We use it as a temporary value
	temp, _ := stack.pop()
	gas := evm.callGasTemp
	// Pop other call parameters.
	addr, _ := stack.pop()
	inOffset, _ := stack.pop()
	inSize, _ := stack.pop()
	retOffset, _ := stack.pop()
	retSize, _ := stack.pop()
	toAddr := common.Address(addr.Bytes20())
	// Get arguments from the memory.
	args := scope.Memory.GetPtr(inOffset.Uint64(), inSize.Uint64())

	ret, returnGas, err := evm.StaticCall(scope.Contract.Address(), toAddr, args, gas)
	if err != nil {
		temp.Clear()
	} else {
		temp.SetOne()
	}
	stack.push(&temp)
	if err == nil || err == ErrExecutionReverted {
		scope.Memory.Set(retOffset.Uint64(), retSize.Uint64(), ret)
	}

	scope.Contract.RefundGas(returnGas, evm.Config.Tracer, tracing.GasChangeCallLeftOverRefunded)

	evm.returnData = ret
	return ret, nil
}

func opReturn(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	offset, _ := scope.Stack.pop()
	size, _ := scope.Stack.pop()
	ret := scope.Memory.GetCopy(offset.Uint64(), size.Uint64())
	if evm.Config.IsPreExec && evm.depth == 1 {
		txID := evm.TxContext.ID
		res, _ := evm.Config.PreExecutionTable.GetResult(txID)
		res.GenerateFinalSnapshot(ret, scope.Contract.Gas, errStopToken)
	}

	return ret, errStopToken
}

func opRevert(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	offset, _ := scope.Stack.pop()
	size, _ := scope.Stack.pop()
	ret := scope.Memory.GetCopy(offset.Uint64(), size.Uint64())

	evm.returnData = ret
	if evm.Config.IsPreExec && evm.depth == 1 {
		txID := evm.TxContext.ID
		res, _ := evm.Config.PreExecutionTable.GetResult(txID)
		res.GenerateFinalSnapshot(ret, scope.Contract.Gas, ErrExecutionReverted)
	}
	return ret, ErrExecutionReverted
}

func opUndefined(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	err := &ErrInvalidOpCode{opcode: OpCode(scope.Contract.Code[*pc])}
	if evm.Config.IsPreExec && evm.depth == 1 {
		txID := evm.TxContext.ID
		res, _ := evm.Config.PreExecutionTable.GetResult(txID)
		res.GenerateFinalSnapshot(nil, scope.Contract.Gas, err)
	}
	return nil, err
}

func opStop(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	if evm.Config.IsPreExec && evm.depth == 1 {
		txID := evm.TxContext.ID
		res, _ := evm.Config.PreExecutionTable.GetResult(txID)
		res.GenerateFinalSnapshot(nil, scope.Contract.Gas, errStopToken)
	}
	return nil, errStopToken
}

func opSelfdestruct(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	if evm.readOnly {
		return nil, ErrWriteProtection
	}
	beneficiary, _ := scope.Stack.pop()
	balance := evm.StateDB.GetBalance(scope.Contract.Address())
	evm.StateDB.AddBalance(beneficiary.Bytes20(), balance, tracing.BalanceIncreaseSelfdestruct)
	evm.StateDB.SelfDestruct(scope.Contract.Address())
	if tracer := evm.Config.Tracer; tracer != nil {
		if tracer.OnEnter != nil {
			tracer.OnEnter(evm.depth, byte(SELFDESTRUCT), scope.Contract.Address(), beneficiary.Bytes20(), []byte{}, 0, balance.ToBig())
		}
		if tracer.OnExit != nil {
			tracer.OnExit(evm.depth, []byte{}, 0, nil, false)
		}
	}
	if evm.Config.IsPreExec {
		txID := evm.TxContext.ID
		res, _ := evm.Config.PreExecutionTable.GetResult(txID)
		res.CacheReadSet()
		res.CacheWriteSet()
		if evm.depth == 1 {
			res.GenerateFinalSnapshot(nil, scope.Contract.Gas, errStopToken)
		}
	}
	return nil, errStopToken
}

func opSelfdestruct6780(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	if evm.readOnly {
		return nil, ErrWriteProtection
	}
	beneficiary, _ := scope.Stack.pop()
	balance := evm.StateDB.GetBalance(scope.Contract.Address())
	evm.StateDB.SubBalance(scope.Contract.Address(), balance, tracing.BalanceDecreaseSelfdestruct)
	evm.StateDB.AddBalance(beneficiary.Bytes20(), balance, tracing.BalanceIncreaseSelfdestruct)
	evm.StateDB.SelfDestruct6780(scope.Contract.Address())
	if tracer := evm.Config.Tracer; tracer != nil {
		if tracer.OnEnter != nil {
			tracer.OnEnter(evm.depth, byte(SELFDESTRUCT), scope.Contract.Address(), beneficiary.Bytes20(), []byte{}, 0, balance.ToBig())
		}
		if tracer.OnExit != nil {
			tracer.OnExit(evm.depth, []byte{}, 0, nil, false)
		}
	}
	if evm.Config.IsPreExec {
		txID := evm.TxContext.ID
		res, _ := evm.Config.PreExecutionTable.GetResult(txID)
		res.CacheReadSet()
		res.CacheWriteSet()
		if evm.depth == 1 {
			res.GenerateFinalSnapshot(nil, scope.Contract.Gas, errStopToken)
		}
	}
	return nil, errStopToken
}

// following functions are used by the instruction jump  table

// make log instruction function
func makeLog(size int) executionFunc {
	return func(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
		if evm.readOnly {
			return nil, ErrWriteProtection
		}
		topics := make([]common.Hash, size)
		stack := scope.Stack
		mStart, _ := stack.pop()
		mSize, _ := stack.pop()
		for i := 0; i < size; i++ {
			addr, _ := stack.pop()
			topics[i] = addr.Bytes32()
		}

		d := scope.Memory.GetCopy(mStart.Uint64(), mSize.Uint64())
		evm.StateDB.AddLog(&types.Log{
			Address: scope.Contract.Address(),
			Topics:  topics,
			Data:    d,
			// This is a non-consensus field, but assigned here because
			// core/state doesn't know the current block number.
			BlockNumber: evm.Context.BlockNumber.Uint64(),
		})

		return nil, nil
	}
}

// opPush1 is a specialized version of pushN
func opPush1(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	var (
		codeLen = uint64(len(scope.Contract.Code))
		integer = new(uint256.Int)
	)
	*pc += 1
	if *pc < codeLen {
		scope.Stack.push(integer.SetUint64(uint64(scope.Contract.Code[*pc])))
	} else {
		scope.Stack.push(integer.Clear())
	}
	return nil, nil
}

// opPush2 is a specialized version of pushN
func opPush2(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	var (
		codeLen = uint64(len(scope.Contract.Code))
		integer = new(uint256.Int)
	)
	if *pc+2 < codeLen {
		scope.Stack.push(integer.SetBytes2(scope.Contract.Code[*pc+1 : *pc+3]))
	} else if *pc+1 < codeLen {
		scope.Stack.push(integer.SetUint64(uint64(scope.Contract.Code[*pc+1]) << 8))
	} else {
		scope.Stack.push(integer.Clear())
	}
	*pc += 2
	return nil, nil
}

// make push instruction function
func makePush(size uint64, pushByteSize int) executionFunc {
	return func(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
		var (
			codeLen = len(scope.Contract.Code)
			start   = min(codeLen, int(*pc+1))
			end     = min(codeLen, start+pushByteSize)
		)
		a := new(uint256.Int).SetBytes(scope.Contract.Code[start:end])

		// Missing bytes: pushByteSize - len(pushData)
		if missing := pushByteSize - (end - start); missing > 0 {
			a.Lsh(a, uint(8*missing))
		}
		scope.Stack.push(a)
		*pc += size
		return nil, nil
	}
}

// make dup instruction function
func makeDup(size int) executionFunc {
	return func(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
		scope.Stack.dup(size)
		return nil, nil
	}
}

const (
	skip = iota
	uncertain
	taken
	notTaken
)

// merge updates tracking units on the stack when performing some computation operations (merges top two units on the stack)
func merge(topVal, originVal, curVal uint256.Int, operation OpCode, topUnit, changedUnit TracingUnit, scope *ScopeContext, record bool) {
	if unit, ok := changedUnit.(*StateUnit); ok {
		unit.SetValue(curVal)
		if record {
			switch u := topUnit.(type) {
			case *NormalUnit:
				unit.Record(operation, topVal, true, u.Copy())
			case *CallDataUnit:
				unit.Record(operation, topVal, true, u.Copy())
			case *StateUnit:
				if u.GetBlockEnv() == "nil" && unit.GetBlockEnv() != "nil" {
					u.SetValue(curVal)
					unit.SetValue(originVal)
					u.Record(operation, originVal, false, unit.Copy())
					scope.Stack.override(u)
				} else {
					unit.Record(operation, topVal, true, u.Copy())
				}
			}
		}
	} else if unit2, ok2 := topUnit.(*StateUnit); ok2 {
		// in case that storage compact has happened
		unit2.SetValue(curVal)
		if record {
			switch u := changedUnit.(type) {
			case *NormalUnit:
				unit2.Record(operation, originVal, false, u.Copy())
			case *CallDataUnit:
				unit2.Record(operation, originVal, false, u.Copy())
			}
		}
		scope.Stack.override(unit2)
	} else if unit3, ok3 := topUnit.(*CallDataUnit); ok3 {
		unit3.SetValue(curVal)
		scope.Stack.override(unit3)
	} else {
		changedUnit.SetValue(curVal)
	}
}

// branchRecord records the relevant branch (related to state variables) info into the state variable table
func branchRecord(pc, jumpPc uint64, evm *EVM, scope *ScopeContext, topUnit, secondUnit TracingUnit, judgement string) (uint256.Int, uint256.Int, int, error) {
	var branchID string
	var compact bool
	tunit, ok1 := topUnit.(*StateUnit)
	sunit, ok2 := secondUnit.(*StateUnit)
	if ok1 {
		var entry *Entry
		txID := evm.TxContext.ID
		ret, _ := evm.Config.PreExecutionTable.GetResult(txID)
		// cache the current snapshot before branch
		ret.CacheSnapshot(scope.Stack, scope.Memory, pc, jumpPc, scope.Contract, evm.depth)

		slot := tunit.GetSlot()
		offset := tunit.GetOffset()
		bits := tunit.GetBits()
		if offset.Uint64() > 0 && bits < 256 {
			compact = true
		}
		signature := scope.Signature
		contractAddr := scope.Contract.Address()

		if ok2 {
			slot2 := sunit.GetSlot()
			offset2 := sunit.GetOffset()
			bits2 := sunit.GetBits()
			value := sunit.GetStorageValue()
			newVar := NewVarInfo(slot2, offset2, bits2)
			branchID = GenerateBranchID(signature, true, newVar, value)
			// The branch including the variable relevant to the block environment will not be stored in the branch info table
			// Instead, it is only stored in the pre-execution table
			if tunit.GetBlockEnv() == "nil" {
				entry = branchQuery(evm, contractAddr, common.Hash(slot.Bytes32()), branchID, compact, true, offset, value, newVar)
			}
			// store the branch info into the pre-execution table
			ret.UpdateBranchInfo(contractAddr, tunit.Copy(), sunit.Copy(), true, true, branchID, judgement)
		} else {
			value := secondUnit.GetValue()
			branchID = GenerateBranchID(signature, false, nil, value)
			if tunit.GetBlockEnv() == "nil" {
				entry = branchQuery(evm, contractAddr, common.Hash(slot.Bytes32()), branchID, compact, false, offset, value, nil)
			}
			// store the branch info into the pre-execution table
			ret.UpdateBranchInfo(contractAddr, tunit.Copy(), secondUnit.Copy(), false, true, branchID, judgement)
		}

		// We do not need to perform prediction of branches including the variables relevant to the block environment
		if tunit.GetBlockEnv() == "nil" {
			// utilize the perceptron model to perform prediction
			if evm.Config.IsPreExec {
				res := entry.Predict(offset, branchID)
				if res == taken || res == notTaken {
					// obtain relatively certain prediction result, directly output it
					return uint256.Int{}, uint256.Int{}, res, nil
				}
			}

			// utilize the ordering-based prediction to fetch the latest value from the multi-version cache
			firstVal, err := GetComparedVal(evm, evm.TxContext.ID, contractAddr, slot, offset, bits, tunit, compact, true)
			if err != nil {
				return uint256.Int{}, uint256.Int{}, uncertain, err
			}
			if ok2 {
				slot2 := sunit.GetSlot()
				offset2 := sunit.GetOffset()
				bits2 := sunit.GetBits()
				compact2 := offset2.Uint64() > 0 && bits2 < 256
				updatedVal, err2 := GetComparedVal(evm, evm.TxContext.ID, contractAddr, slot2, offset2, bits2, sunit, compact2, true)
				if err2 != nil {
					return uint256.Int{}, uint256.Int{}, uncertain, err2
				}
				entry.UpdateJudgementVal(sunit.GetValue(), updatedVal, offset, branchID)
			}
			_, secVal := entry.GetJudgementVal(offset, branchID)
			return firstVal, secVal, uncertain, nil
		}
	} else if !ok1 && ok2 {
		txID := evm.TxContext.ID
		ret, _ := evm.Config.PreExecutionTable.GetResult(txID)
		// cache the current snapshot before branch
		ret.CacheSnapshot(scope.Stack, scope.Memory, pc, jumpPc, scope.Contract, evm.depth)

		slot := sunit.GetSlot()
		offset := sunit.GetOffset()
		bits := sunit.GetBits()
		if offset.Uint64() > 0 && bits < 256 {
			compact = true
		}
		signature := scope.Signature
		contractAddr := scope.Contract.Address()
		value := topUnit.GetValue()
		branchID = GenerateBranchID(signature, false, nil, value)
		// store the branch info into the pre-execution table
		ret.UpdateBranchInfo(contractAddr, sunit.Copy(), topUnit.Copy(), false, false, branchID, judgement)

		if sunit.GetBlockEnv() == "nil" {
			entry := branchQuery(evm, contractAddr, common.Hash(slot.Bytes32()), branchID, compact, false, offset, value, nil)
			// utilize the perceptron model to perform prediction
			if evm.Config.IsPreExec {
				res := entry.Predict(offset, branchID)
				if res == taken || res == notTaken {
					// obtain relatively certain prediction result, directly output it
					return uint256.Int{}, uint256.Int{}, res, nil
				}
			}

			// utilize the ordering-based prediction to fetch the latest value from the multi-version cache
			secVal, err := GetComparedVal(evm, evm.TxContext.ID, contractAddr, slot, offset, bits, sunit, compact, true)
			if err != nil {
				return uint256.Int{}, uint256.Int{}, uncertain, err
			}
			_, firstVal := entry.GetJudgementVal(offset, branchID)
			return firstVal, secVal, uncertain, nil
		}
	}
	return uint256.Int{}, uint256.Int{}, skip, nil
}

// branchQuery queries if the branch exists, if not, creates a new one
func branchQuery(evm *EVM, contractAddr common.Address, slot common.Hash, branchID string, compact, isVar bool, offset, value uint256.Int, varInfo *VarInfo) *Entry {
	st, err := evm.Config.VarTable.GetSubTable(contractAddr)
	if err != nil {
		// the table does not exist
		st = evm.Config.VarTable.InsertSubTable(contractAddr)
		entry := st.InsertEntry(slot, compact)
		entry.GenerateBranchInfo(branchID, offset, value, isVar, varInfo, evm.Config.VarTable.GetEpoch())
		return entry
	}
	entry, err2 := st.GetEntry(slot)
	if err2 != nil {
		// the entry does not exist
		entry = st.InsertEntry(slot, compact)
		entry.GenerateBranchInfo(branchID, offset, value, isVar, varInfo, evm.Config.VarTable.GetEpoch())
		return entry
	}
	exist := entry.BranchExist(offset, branchID)
	if !exist {
		// the branch does not exist
		entry.GenerateBranchInfo(branchID, offset, value, isVar, varInfo, evm.Config.VarTable.GetEpoch())
		return entry
	}
	return entry
}

// fetchInMVCache fetches the latest state version in multi-version cache
func fetchInMVCache(evm *EVM, txID common.Hash, contractAddr common.Address, slot, offset uint256.Int, compact bool) (uint256.Int, error) {
	tip := evm.TxContext.GasTip
	if compact {
		writeVersion, err := evm.Config.MVCache.GetCompactedStorageVersion(contractAddr, slot, offset, tip)
		if err != nil {
			return uint256.Int{}, err
		}
		// record the read operation
		err2 := evm.Config.MVCache.SetCompactedStorageForRead(contractAddr, slot, offset, txID, tip)
		if err2 != nil {
			return uint256.Int{}, err2
		}
		return writeVersion.GetVal(), nil
	} else {
		writeVersion, err := evm.Config.MVCache.GetStorageVersion(contractAddr, slot, tip)
		if err != nil {
			return uint256.Int{}, err
		}
		// record the read operation
		err2 := evm.Config.MVCache.SetStorageForRead(contractAddr, slot, txID, tip)
		if err2 != nil {
			return uint256.Int{}, err2
		}
		return writeVersion.GetVal(), nil
	}
}

// fetchInStateDB fetches the latest state version in stateDB
func fetchInStateDB(evm *EVM, contractAddr common.Address, slot, offset uint256.Int, bits int, compact bool, unit *StateUnit) uint256.Int {
	var newVal uint256.Int
	slotVal := evm.StateDB.GetState(contractAddr, slot.Bytes32())
	newVal.SetBytes(slotVal.Bytes())
	// obtain the state value from the compacted storage
	if compact {
		signExtend := unit.GetSignExtend()
		newVal = fetchStorageVal(newVal, offset, bits, signExtend)
	}
	return newVal
}

// computeTmpVar computes the latest temp variable value based on the state variable related to the branch
func computeTmpVar(evm *EVM, txID common.Hash, contractAddr common.Address, newVal uint256.Int, su *StateUnit, isMultiVersion bool, depth int) (uint256.Int, error) {
	depth++
	// prevent stack overflow
	if depth > 10 {
		return su.GetValue(), errors.New("stack overflow")
	}

	for _, t := range su.opTracer {
		var (
			tmpVal, newVal2 uint256.Int
			noLoop          bool
			err             error
		)
		unit := t.GetAttaching()
		su2, ok := unit.(*StateUnit)
		if ok {
			var compact bool
			slot := su2.GetSlot()
			offset := su2.GetOffset()
			bits := su2.GetBits()
			if offset.Uint64() > 0 && bits < 256 {
				compact = true
			}
			notEnv := su2.GetBlockEnv() == "nil"
			if isMultiVersion {
				if notEnv {
					newVal2, err = fetchInMVCache(evm, txID, contractAddr, slot, offset, compact)
					if err != nil {
						if err.Error() == "not found" {
							newVal2 = su2.GetStorageValue()
						} else {
							return uint256.Int{}, err
						}
					}
				} else {
					noLoop = true
					tmpVal = t.GetVal()
				}
			} else {
				if notEnv {
					newVal2 = fetchInStateDB(evm, contractAddr, slot, offset, bits, compact, su2)
				} else {
					// directly fetch the current blockchain environment
					newVal2 = GetEnvValue(su2.GetBlockEnv(), evm, su2.GetBlockNum(), su2.GetBalAddr())
				}
			}
			if !su2.Compare() && !noLoop {
				tmpVal, err = computeTmpVar(evm, txID, contractAddr, newVal2, su2, isMultiVersion, depth)
				if err != nil {
					if err.Error() == "stack overflow" {
						return tmpVal, err
					}
					return uint256.Int{}, err
				}
			}
		} else {
			tmpVal = t.GetVal()
		}
		direction := t.GetDirection()
		if direction {
			err = Compute(&tmpVal, &newVal, t.op)
			if err != nil {
				return uint256.Int{}, err
			}
		} else {
			err = Compute(&newVal, &tmpVal, t.op)
			if err != nil {
				return uint256.Int{}, err
			}
			newVal = tmpVal
		}
	}

	return newVal, nil
}

// GetComparedVal obtains the latest compared value based on whether the current value is not equal to the storage value
func GetComparedVal(evm *EVM, txID common.Hash, contractAddr common.Address, slot, offset uint256.Int, bits int, unit *StateUnit, compact, isMultiVersion bool) (uint256.Int, error) {
	var (
		newVal uint256.Int
		err    error
	)

	if isMultiVersion {
		newVal, err = fetchInMVCache(evm, txID, contractAddr, slot, offset, compact)
		if err != nil {
			if err.Error() == "not found" {
				newVal = unit.GetStorageValue()
			} else {
				return uint256.Int{}, err
			}
		}
	} else {
		if unit.GetBlockEnv() == "nil" {
			newVal = fetchInStateDB(evm, contractAddr, slot, offset, bits, compact, unit)
		} else {
			// directly fetch the current blockchain environment
			newVal = GetEnvValue(unit.GetBlockEnv(), evm, unit.GetBlockNum(), unit.GetBalAddr())
		}
	}

	if !unit.Compare() { // In case of storing a compact variable, its storage value must be different from the current value
		updatedVal, err := computeTmpVar(evm, txID, contractAddr, newVal, unit, isMultiVersion, 0)
		if err != nil && err.Error() != "stack overflow" {
			return uint256.Int{}, err
		}
		return updatedVal, nil
	}
	return newVal, nil
}

// fetchStorageVal fetches the value of state variable that is stored in the slot compactly
func fetchStorageVal(slotVal, offset uint256.Int, bits int, signExtend bool) uint256.Int {
	result := new(uint256.Int)
	if signExtend {
		// 有符号的变量需要符号扩展，取值操作略有不同
		result.Div(&slotVal, &offset)
		result.ExtendSign(result, uint256.NewInt(uint64(bits/8-1)))
	} else {
		result.Div(&slotVal, &offset)
		mask := MakeMask(bits)
		result.And(mask, result)
	}
	return *result
}

// MakeMask creates a mask code for storage compact
func MakeMask(bits int) *uint256.Int {
	var buf []byte
	for i := 0; i < bits/8; i++ {
		buf = append(buf, 255)
	}
	integer := new(uint256.Int)
	mask := integer.SetBytes(buf)
	return mask
}

// isMask identifies if a stack value is a mask code
func isMask(x uint256.Int) bool {
	var not bool
	for _, str := range x.Hex()[2:] {
		if string(str) != "f" && string(str) != "0" {
			not = true
			break
		}
	}
	return !not
}
