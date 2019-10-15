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
	available  chan struct{}
	udp        *net.UDPConn
	lastActive time.Time
}

// Forwarder represents a UDP packet forwarder.
type Forwarder struct {
	src          *net.UDPAddr
	dst          *net.UDPAddr
	client       *net.UDPAddr
	listenerConn *net.UDPConn

	connections      map[string]*connection
	connectionsMutex *sync.RWMutex

	connectCallback    func(addr string)
	disconnectCallback func(addr string)

	timeout time.Duration

	closed bool
}

// DefaultTimeout is the default timeout period of inactivity for convenience
// sake. It is equivelant to 5 minutes.
const DefaultTimeout = time.Minute * 5

// Forward forwards UDP packets from the src address to the dst address, with a
// timeout to "disconnect" clients after the timeout period of inactivity. It
// implements a reverse NAT and thus supports multiple seperate users. Forward
// is also asynchronous.
func Forward(src, dst string, timeout time.Duration) (*Forwarder, error) {
	forwarder := new(Forwarder)
	forwarder.connectCallback = func(addr string) {}
	forwarder.disconnectCallback = func(addr string) {}
	forwarder.connectionsMutex = new(sync.RWMutex)
	forwarder.connections = make(map[string]*connection)
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
		oob := make([]byte, bufferSize)
		n, _, _, addr, err := f.listenerConn.ReadMsgUDP(buf, oob)
		if err != nil {
			log.Println("forward: failed to read, terminating:", err)
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
	f.connectionsMutex.Lock()
	conn, found := f.connections[addr.String()]
	if !found {
		f.connections[addr.String()] = &connection{
			available:  make(chan struct{}),
			udp:        nil,
			lastActive: time.Now(),
		}
	}
	f.connectionsMutex.Unlock()

	if !found {
		var udpConn *net.UDPConn
		var err error
		if f.dst.IP.To4()[0] == 127 {
			// log.Println("using local listener")
			laddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:")
			udpConn, err = net.DialUDP("udp", laddr, f.dst)
		} else {
			udpConn, err = net.DialUDP("udp", nil, f.dst)
		}
		if err != nil {
			log.Println("udp-forward: failed to dial:", err)
			delete(f.connections, addr.String())
			return
		}

		f.connectionsMutex.Lock()
		f.connections[addr.String()].udp = udpConn
		f.connections[addr.String()].lastActive = time.Now()
		close(f.connections[addr.String()].available)
		f.connectionsMutex.Unlock()

		f.connectCallback(addr.String())

		_, _, err = udpConn.WriteMsgUDP(data, nil, nil)
		if err != nil {
			log.Println("udp-forward: error sending initial packet to client", err)
		}

		for {
			// log.Println("in loop to read from NAT connection to servers")
			buf := make([]byte, bufferSize)
			oob := make([]byte, bufferSize)
			n, _, _, _, err := udpConn.ReadMsgUDP(buf, oob)
			if err != nil {
				f.connectionsMutex.Lock()
				udpConn.Close()
				delete(f.connections, addr.String())
				f.connectionsMutex.Unlock()
				f.disconnectCallback(addr.String())
				log.Println("udp-forward: abnormal read, closing:", err)
				return
			}

			// log.Println("sent packet to client")
			_, _, err = f.listenerConn.WriteMsgUDP(buf[:n], nil, addr)
			if err != nil {
				log.Println("udp-forward: error sending packet to client:", err)
			}
		}

		// unreachable
	}

	<-conn.available

	// log.Println("sent packet to server", conn.udp.RemoteAddr())
	_, _, err := conn.udp.WriteMsgUDP(data, nil, nil)
	if err != nil {
		log.Println("udp-forward: error sending packet to server:", err)
	}

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

// Close stops the forwarder.
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

// OnDisconnect can be called with a callback function to be called whenever a
// new client disconnects (after 5 minutes of inactivity).
func (f *Forwarder) OnDisconnect(callback func(addr string)) {
	f.disconnectCallback = callback
}

// Connected returns the list of connected clients in IP:port form.
func (f *Forwarder) Connected() []string {
	f.connectionsMutex.Lock()
	defer f.connectionsMutex.Unlock()
	results := make([]string, 0, len(f.connections))
	for key := range f.connections {
		results = append(results, key)
	}
	return results
}
