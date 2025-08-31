package netlimit

import (
	"net"
	"time"
)

type limitedConn struct {
	net.Conn
	latency time.Duration
	limiter *Limiter
}

func (c *limitedConn) Write(data []byte) (int, error) {
	if c.limiter.bandwidth > 0 {
		c.limiter.Wait(c, int64(len(data)))
	}
	time.Sleep(c.latency)
	return c.Conn.Write(data)
}

func (c *limitedConn) Close() error {
	return c.Conn.Close()
}
