package vm

import (
	"container/list"
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

// MVCache caches all read/written state variables in branches in a multi-version format
// Only used in pre-execution for ordering-based prediction and pre-execution repair
type MVCache struct {
	Cache map[common.AddrU256]*MVObject
}

// NewMVCache initiates a new multi-version cache
func NewMVCache() *MVCache {
	return &MVCache{
		Cache: make(map[common.AddrU256]*MVObject),
	}
}

// GetObject gets an existing multi-version object
func (mvc *MVCache) GetObject(addr common.Address, key uint256.Int) *MVObject {
	return mvc.Cache[common.AddrU256{Addr: addr, U256: key}]
}

// GetOrCreateObject gets an existing multi-version object, if not, creates a new one
func (mvc *MVCache) GetOrCreateObject(addr common.Address, key uint256.Int, isCompact bool) *MVObject {
	mvo, ok := mvc.Cache[common.AddrU256{Addr: addr, U256: key}]
	if !ok {
		mvo = NewMVObject(isCompact)
		mvc.Cache[common.AddrU256{Addr: addr, U256: key}] = mvo
	}
	return mvo
}

func (mvc *MVCache) SetStorageForRead(addr common.Address, key uint256.Int, txID common.Hash, tip *big.Int) error {
	obj := mvc.GetOrCreateObject(addr, key, false)
	if obj.slotStorage == nil {
		obj.slotStorage = newMVRecord()
	}
	return obj.setStorageForRead(txID, tip)
}

func (mvc *MVCache) SetStorageForWrite(addr common.Address, key, value uint256.Int, txID common.Hash, tip *big.Int) (bool, error) {
	obj := mvc.GetOrCreateObject(addr, key, false)
	if obj.slotStorage == nil {
		obj.slotStorage = newMVRecord()
	}
	return obj.setStorageForWrite(txID, value, tip)
}

func (mvc *MVCache) SetCompactedStorageForRead(addr common.Address, key, offset uint256.Int, txID common.Hash, tip *big.Int) error {
	obj := mvc.GetOrCreateObject(addr, key, true)
	if obj.compactedStorage == nil {
		obj.compactedStorage = newCompactedStorage()
	}
	return obj.setCompactedStorageForRead(offset, txID, tip)
}

func (mvc *MVCache) SetCompactedStorageForWrite(addr common.Address, key, offset, value uint256.Int, txID common.Hash, tip *big.Int) (bool, error) {
	obj := mvc.GetOrCreateObject(addr, key, true)
	if obj.compactedStorage == nil {
		obj.compactedStorage = newCompactedStorage()
	}
	return obj.setCompactedStorageForWrite(txID, offset, value, tip)
}

func (mvc *MVCache) GetStorageVersion(addr common.Address, key uint256.Int, tip *big.Int) (*WriteVersion, error) {
	obj := mvc.GetObject(addr, key)
	if obj == nil {
		return nil, errors.New("not found")
	}
	return obj.getStorageVersion(tip)
}

func (mvc *MVCache) GetCompactedStorageVersion(addr common.Address, key, offset uint256.Int, tip *big.Int) (*WriteVersion, error) {
	obj := mvc.GetObject(addr, key)
	if obj == nil {
		return nil, errors.New("not found")
	}
	return obj.getCompactedStorageVersion(offset, tip)
}

type MVObject struct {
	slotStorage      *mvRecord
	compactedStorage *compactedStorage
	isCompact        bool
}

func NewMVObject(isCompact bool) *MVObject {
	mvo := &MVObject{}
	if !isCompact {
		mvo.slotStorage = newMVRecord()
	} else {
		mvo.compactedStorage = newCompactedStorage()
	}
	mvo.isCompact = isCompact
	return mvo
}

// setStorageForRead inserts a read record into the correct location of the list
func (mvo *MVObject) setStorageForRead(txID common.Hash, tip *big.Int) error {
	return mvo.slotStorage.insertReadRecord(txID, tip)
}

// setStorageForWrite inserts a write record into the correct location of the list
// return the true value of 'repair' indicates that the remaining txs after this tx requires repair operation
func (mvo *MVObject) setStorageForWrite(txID common.Hash, value uint256.Int, tip *big.Int) (bool, error) {
	return mvo.slotStorage.insertWriteRecord(txID, value, tip)
}

// setCompactedStorageForRead inserts a read record into the corresponding location of the list (for compacted storage slot)
func (mvo *MVObject) setCompactedStorageForRead(offset uint256.Int, txID common.Hash, tip *big.Int) error {
	rd, ok := mvo.compactedStorage.offsetMap[offset.String()]
	if !ok {
		rd = newMVRecord()
		mvo.compactedStorage.offsetMap[offset.String()] = rd
	}
	return rd.insertReadRecord(txID, tip)
}

// setCompactedStorageForWrite inserts a write record into the corresponding location of the list (for compacted storage slot)
// return the true value of 'repair' indicates that the remaining txs after this tx requires repair operation
func (mvo *MVObject) setCompactedStorageForWrite(txID common.Hash, offset, value uint256.Int, tip *big.Int) (bool, error) {
	rd, ok := mvo.compactedStorage.offsetMap[offset.String()]
	if !ok {
		rd = newMVRecord()
		mvo.compactedStorage.offsetMap[offset.String()] = rd
	}
	return rd.insertWriteRecord(txID, value, tip)
}

// getStorageVersion obtains the storage version at the corresponding location in the list
func (mvo *MVObject) getStorageVersion(tip *big.Int) (*WriteVersion, error) {
	if mvo.slotStorage == nil {
		return nil, errors.New("not found")
	}
	return mvo.slotStorage.getWriteVersion(tip)
}

// getCompactedStorageVersion obtains the storage version at the corresponding location in the list (for compacted storage slot)
func (mvo *MVObject) getCompactedStorageVersion(offset uint256.Int, tip *big.Int) (*WriteVersion, error) {
	if mvo.compactedStorage == nil {
		return nil, errors.New("not found")
	}
	rd, ok := mvo.compactedStorage.offsetMap[offset.String()]
	if !ok {
		rd = newMVRecord()
		mvo.compactedStorage.offsetMap[offset.String()] = rd
	}
	return rd.getWriteVersion(tip)
}

type compactedStorage struct {
	offsetMap map[string]*mvRecord
}

func newCompactedStorage() *compactedStorage {
	return &compactedStorage{
		offsetMap: make(map[string]*mvRecord),
	}
}

type mvRecord struct {
	rRecord    *list.List
	wRecord    *list.List
	rLoc       *list.Element // latest location of read (itself)
	wLoc       *list.Element // latest location of write (itself)
	readRepair bool
}

func newMVRecord() *mvRecord {
	return &mvRecord{
		rRecord:    list.New(),
		wRecord:    list.New(),
		rLoc:       nil,
		wLoc:       nil,
		readRepair: false,
	}
}

func (r *mvRecord) insertReadRecord(txID common.Hash, tip *big.Int) error {
	newVer := &ReadVersion{txID: txID, tip: tip}
	counter := r.rRecord.Len()
	if counter == 0 {
		r.rRecord.PushBack(newVer)
		return nil
	}
	for e := r.rRecord.Back(); e != nil; e = e.Prev() {
		ver, ok := e.Value.(*ReadVersion)
		if !ok {
			return errors.New("wrong version format")
		}
		if (tip.Cmp(ver.GetTip()) == -1) || (tip.Cmp(ver.GetTip()) == 0 && txID != ver.txID) {
			// 待插入交易的交易费小于某个交易元素或者两个交易的交易费相同，插入到此交易元素之后
			r.rRecord.InsertAfter(newVer, e)
			break
		} else if counter == 1 && tip.Cmp(ver.GetTip()) == 1 {
			// 插入列表的第一位
			r.rRecord.InsertBefore(newVer, e)
			break
		} else if txID == ver.txID {
			// 发现列表中已经插入该交易元素，退出
			break
		}
		counter--
	}
	return nil
}

// checkReadRecord used for repair check
func (r *mvRecord) checkReadRecord(tip *big.Int) (bool, error) {
	counter := r.rRecord.Len()
	if counter == 0 {
		return false, nil
	}
	for e := r.rRecord.Back(); e != nil; e = e.Prev() {
		ver, ok := e.Value.(*ReadVersion)
		if !ok {
			return false, errors.New("wrong version format")
		}
		if (tip.Cmp(ver.GetTip()) == -1) || (tip.Cmp(ver.GetTip()) == 0) {
			r.rLoc = e.Next()
			if r.rLoc != nil {
				return true, nil
			}
			break
		} else if counter == 1 && tip.Cmp(ver.GetTip()) == 1 {
			r.rLoc = e
			return true, nil
		}
		counter--
	}
	return false, nil
}

// 插入写版本，队尾还有元素，修复队尾那些交易，并修复所有读了队尾那些交易的交易 (不考虑级联修复)；
// 并检查是否有交易读了比此版本小的版本，如果交易的排序更后的话，修复这些交易
// 综上，只需要检查读版本中是否有交易排序在插入写版本交易的后面，这些交易将被修复
func (r *mvRecord) insertWriteRecord(txID common.Hash, value uint256.Int, tip *big.Int) (bool, error) {
	readRepair, err := r.checkReadRecord(tip)
	r.readRepair = readRepair
	if err != nil {
		return false, err
	}
	newVer := &WriteVersion{txID: txID, value: value, tip: tip}
	counter := r.wRecord.Len()
	if counter == 0 {
		curE := r.wRecord.PushBack(newVer)
		r.wLoc = curE
		return false, nil
	}
	for e := r.wRecord.Back(); e != nil; e = e.Prev() {
		ver, ok := e.Value.(*WriteVersion)
		if !ok {
			return false, errors.New("wrong version format")
		}
		if (tip.Cmp(ver.GetTip()) == -1) || (tip.Cmp(ver.GetTip()) == 0 && txID != ver.txID) {
			// 待插入交易的交易费小于某个交易元素或者两个交易的交易费相同，插入到此交易元素之后
			curE := r.wRecord.InsertAfter(newVer, e)
			r.wLoc = curE
			// 通知需要将后面的交易进行预执行修复
			if curE.Next() != nil || r.readRepair {
				return true, nil
			}
			break
		} else if counter == 1 && tip.Cmp(ver.GetTip()) == 1 {
			// 插入列表的第一位
			curE := r.wRecord.InsertBefore(newVer, e)
			r.wLoc = curE
			// 通知需要将后面的交易进行预执行修复
			return true, nil
		} else if txID == ver.txID {
			// 发现列表中已经插入该交易元素，修改版本信息（写入值）
			ver.value = value
			break
		}
		counter--
	}
	return false, nil
}

func (r *mvRecord) getWriteVersion(tip *big.Int) (*WriteVersion, error) {
	for e := r.wRecord.Back(); e != nil; e = e.Prev() {
		ver, ok := e.Value.(*WriteVersion)
		if !ok {
			return nil, errors.New("wrong version format")
		}
		if tip.Cmp(ver.GetTip()) == -1 || tip.Cmp(ver.GetTip()) == 0 {
			// 遇到费用比自己大的交易或者费用相等的交易，直接读取其写入的版本
			return ver, nil
		}
	}
	return nil, errors.New("not found")
}

type WriteVersion struct {
	txID  common.Hash
	value uint256.Int
	tip   *big.Int
}

func (wv *WriteVersion) GetVal() uint256.Int { return wv.value }
func (wv *WriteVersion) GetTip() *big.Int    { return wv.tip } // Get the gas fee of transaction

type ReadVersion struct {
	txID common.Hash
	tip  *big.Int
}

func (rv *ReadVersion) GetTip() *big.Int { return rv.tip } // Get the gas fee of transaction
