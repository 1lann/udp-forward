// Package forward contains a UDP packet forwarder.
package forward

import (
	"log"
	"net"
	"sync"
	"time"
)

const bufferSize = 4096

type connection struct {
	udp        *net.UDPConn
	lastActive time.Time
}

// Forwarder represents a UDP packet forwarder.
type Forwarder struct {
	src          *net.UDPAddr
	dst          *net.UDPAddr
	client       *net.UDPAddr
	listenerConn *net.UDPConn

	connections      map[string]connection
	connectionsMutex *sync.RWMutex

	connectCallback    func(addr string)
	disconnectCallback func(addr string)

	timeout time.Duration

	closed bool
}

// DefaultTimeout is the default timeout period of inactivity for convenience
// sake. It is equivelant to 5 minutes.
var DefaultTimeout = time.Minute * 5

// Forward forwards UDP packets from the src address to the dst address, with a
// timeout to "disconnect" clients after the timeout period of inactivity. It
// implements a reverse NAT and thus supports multiple seperate users. Forward
// is also asynchronous.
func Forward(src, dst string, timeout time.Duration) (*Forwarder, error) {
	forwarder := new(Forwarder)
	forwarder.connectionsMutex = new(sync.RWMutex)
	forwarder.connections = make(map[string]connection)
	forwarder.timeout = timeout

	var err error
	forwarder.src, err = net.ResolveUDPAddr("udp", src)
	if err != nil {
		return nil, err
	}

	forwarder.dst, err = net.ResolveUDPAddr("udp", dst)
	if err != nil {
		return nil, err
	}

	forwarder.client = &net.UDPAddr{
		IP:   forwarder.src.IP,
		Port: 0,
		Zone: forwarder.src.Zone,
	}

	forwarder.listenerConn, err = net.ListenUDP("udp", forwarder.src)
	if err != nil {
		return nil, err
	}

	go forwarder.janitor()
	go forwarder.run()

	return forwarder, nil
}

func (f *Forwarder) run() {
	for {
		buf := make([]byte, bufferSize)
		n, addr, err := f.listenerConn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		go f.handle(buf[:n], addr)
	}
}

func (f *Forwarder) janitor() {
	for !f.closed {
		time.Sleep(f.timeout)
		var keysToDelete []string

		f.connectionsMutex.RLock()
		for k, conn := range f.connections {
			if conn.lastActive.Before(time.Now().Add(-f.timeout)) {
				keysToDelete = append(keysToDelete, k)
			}
		}
		f.connectionsMutex.RUnlock()

		f.connectionsMutex.Lock()
		for _, k := range keysToDelete {
			f.connections[k].udp.Close()
			delete(f.connections, k)
		}
		f.connectionsMutex.Unlock()

		for _, k := range keysToDelete {
			f.disconnectCallback(k)
		}
	}
}

func (f *Forwarder) handle(data []byte, addr *net.UDPAddr) {
	f.connectionsMutex.RLock()
	conn, found := f.connections[addr.String()]
	f.connectionsMutex.RUnlock()

	if !found {
		conn, err := net.ListenUDP("udp", f.client)
		if err != nil {
			log.Println("udp-forwader: failed to dial:", err)
			return
		}

		f.connectionsMutex.Lock()
		f.connections[addr.String()] = connection{
			udp:        conn,
			lastActive: time.Now(),
		}
		f.connectionsMutex.Unlock()

		f.connectCallback(addr.String())

		conn.WriteTo(data, f.dst)

		for {
			buf := make([]byte, bufferSize)
			n, _, err := conn.ReadFromUDP(buf)
			if err != nil {
				f.connectionsMutex.Lock()
				conn.Close()
				delete(f.connections, addr.String())
				f.connectionsMutex.Unlock()
				return
			}

			go func(data []byte, conn *net.UDPConn, addr *net.UDPAddr) {
				f.listenerConn.WriteTo(data, addr)
			}(buf[:n], conn, addr)
		}

		return
	}

	conn.udp.WriteTo(data, f.dst)

	shouldChangeTime := false
	f.connectionsMutex.RLock()
	if _, found := f.connections[addr.String()]; found {
		if f.connections[addr.String()].lastActive.Before(
			time.Now().Add(f.timeout / 4)) {
			shouldChangeTime = true
		}
	}
	f.connectionsMutex.RUnlock()

	if shouldChangeTime {
		f.connectionsMutex.Lock()
		// Make sure it still exists
		if _, found := f.connections[addr.String()]; found {
			connWrapper := f.connections[addr.String()]
			connWrapper.lastActive = time.Now()
			f.connections[addr.String()] = connWrapper
		}
		f.connectionsMutex.Unlock()
	}
}

func (f *Forwarder) Close() {
	f.connectionsMutex.Lock()
	f.closed = true
	for _, conn := range f.connections {
		conn.udp.Close()
	}
	f.listenerConn.Close()
	f.connectionsMutex.Unlock()
}

// OnConnect can be called with a callback function to be called whenever a
// new client connects.
func (f *Forwarder) OnConnect(callback func(addr string)) {
	f.connectCallback = callback
}

// OnConnect can be called with a callback function to be called whenever a
// new client disconnects (after 5 minutes of inactivity).
func (f *Forwarder) OnDisconnect(callback func(addr string)) {
	f.disconnectCallback = callback
}

// Connected returns the list of connected clients in IP:port form.
func (f *Forwarder) Connected() []string {
	f.connectionsMutex.Lock()
	results := make([]string, 0, len(f.connections))
	for key, _ := range f.connections {
		results = append(results, key)
	}
	return results
}
