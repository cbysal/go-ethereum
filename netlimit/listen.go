package netlimit

import "net"

type Listener struct {
	net.Listener
	limiter *Limiter
}

func (l *Listener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	lenByte := make([]byte, 1)
	if _, err = conn.Read(lenByte); err != nil {
		return nil, err
	}
	addrBytes := make([]byte, lenByte[0])
	if _, err = conn.Read(addrBytes); err != nil {
		return nil, err
	}
	addr := string(addrBytes)
	latency := l.limiter.latencies[addr]
	return &limitedConn{
		Conn:    conn,
		latency: latency,
		limiter: l.limiter,
	}, nil
}
