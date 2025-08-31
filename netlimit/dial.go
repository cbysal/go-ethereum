package netlimit

import (
	"context"
	"net"
)

type Dialer struct {
	net.Dialer
	limiter *Limiter
}

func (d *Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := d.Dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	addr, err := net.ResolveTCPAddr(network, address)
	if err != nil {
		return nil, err
	}
	latency := d.limiter.latencies[addr.String()]
	data := []byte(d.limiter.localAddr.String())
	data = append([]byte{byte(len(data))}, data...)
	if _, err = conn.Write(data); err != nil {
		return nil, err
	}
	return &limitedConn{
		Conn:    conn,
		latency: latency,
		limiter: d.limiter,
	}, nil
}
