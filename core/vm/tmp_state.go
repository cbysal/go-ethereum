package vm

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

// TmpState defines the temporary state space that stores all accessed and updated state slots
// In sstore operation, we can easily obtain the variable information according to storage slot
type TmpState struct {
	Space map[common.AddrU256]*Fragment
}

type Fragment struct {
	isCompact bool
}

func NewTmpState() *TmpState {
	tmp := &TmpState{Space: make(map[common.AddrU256]*Fragment)}
	return tmp
}

func (ts *TmpState) GetFragment(addr common.Address, key uint256.Int) (*Fragment, bool) {
	fragment, ok := ts.Space[common.AddrU256{Addr: addr, U256: key}]
	return fragment, ok
}

func (ts *TmpState) InsertUnit(addr common.Address, key uint256.Int) *Fragment {
	fragment, ok := ts.Space[common.AddrU256{Addr: addr, U256: key}]
	if !ok {
		fragment = NewFragment()
		ts.Space[common.AddrU256{Addr: addr, U256: key}] = fragment
	}
	return fragment
}

func NewFragment() *Fragment {
	frag := &Fragment{
		isCompact: false,
	}
	return frag
}

func (f *Fragment) GetCompact() bool { return f.isCompact }

func (f *Fragment) GenerateVar(stateUnit *StateUnit) {
	offset := stateUnit.GetOffset()
	bits := stateUnit.GetBits()
	if offset.Uint64() > 0 && bits < 256 {
		f.isCompact = true
	}
}
