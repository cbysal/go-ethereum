package conf

import (
	"sync/atomic"
)

var (
	MatchBlock     float64 = 1
	MatchTx        float64 = 1
	SendBlockCount atomic.Int32
)
