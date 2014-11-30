package mp

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
)

const (
	errStringUnknownProtocol = "unknown protocol"
	errStringMultipleAuths   = "client has already authenticated"
	errStringNotYetAuthed    = "client not yet authenticated"
)

type clientConnection struct {
	closed        chan struct{}
	isClosed      uint32
	isEstablished uint32

	messages       chan []byte
	currentMessage []byte
	readLock       sync.Mutex

	otherClient string
	connID      string
	client      *Client
}

var (
	errWouldBlock = errors.New("would block")
)

func newClientConnection(otherClient, connID string, client *Client) *clientConnection {
	return &clientConnection{
		// messages needs to remain unbuffered because of how our closed channel works.
		messages:    make(chan []byte),
		closed:      make(chan struct{}),
		otherClient: otherClient,
		connID:      connID,
		client:      client,
	}
}

func (c *clientConnection) isHandshakeComplete() bool {
	return atomic.LoadUint32(&c.isEstablished) != 0
}

func (c *clientConnection) noteHandshakeComplete() {
	atomic.StoreUint32(&c.isEstablished, 1)
}

func (c *clientConnection) readIntoBuffer(blocking bool) error {
	if c.currentMessage != nil {
		return nil
	}

	var msg []byte
	if !blocking {
		select {
		case msg = <-c.messages:
			break
		case <-c.closed:
			return io.EOF
		default:
			return errWouldBlock
		}
	} else {
		select {
		case msg = <-c.messages:
			break
		case <-c.closed:
			return io.EOF
		}
	}

	c.currentMessage = msg
	return nil
}

func (c *clientConnection) putNewMessage(msg *Message) bool {
	select {
	case <-c.closed:
		return false
	case c.messages <- msg.Data:
		return true
	}
}

func (c *clientConnection) Read(b []byte) (int, error) {
	c.readLock.Lock()
	defer c.readLock.Unlock()

	n := 0
	remSlice := b
	for len(remSlice) > 0 {
		block := n == 0
		err := c.readIntoBuffer(block)
		if err == errWouldBlock {
			return n, nil
		} else if err != nil {
			return n, err
		}

		x := copy(remSlice, c.currentMessage)
		if len(c.currentMessage) == x {
			// Release the message instead of letting it linger as a 0 size slice.
			c.currentMessage = nil
		} else {
			c.currentMessage = c.currentMessage[x:]
		}
		remSlice = remSlice[x:]
		n += x
	}

	return n, nil
}

func (c *clientConnection) Write(b []byte) (int, error) {
	if err := c.WriteMessage(b); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *clientConnection) Close() error {
	if atomic.CompareAndSwapUint32(&c.isClosed, 0, 1) {
		close(c.closed)
		// nil check because testing can nil out the client
		if client := c.client; client != nil {
			client.notifyClosed(c)
		}
	}
	return nil
}

// Micro-optimization: Lots of times, the Client will call Close() on
// connections after removing it from the Clients list. There's no point
// in having Close() try to remove itself from a list it's already been removed
// from, so we skip that step in this close.
//
// Note: it should not be *assumed* that if this is called, the client will
// not be notified by a different goroutine -- there's still a chance that
// another goroutine will concurrently call Close() before this can be called.
// The only thing this saves us from is notifying the Client *in the current
// goroutine*.
func (c *clientConnection) closeNoNotify() error {
	if atomic.CompareAndSwapUint32(&c.isClosed, 0, 1) {
		close(c.closed)
	}
	return nil
}

func (c *clientConnection) ReadMessage() ([]byte, error) {
	c.readLock.Lock()
	defer c.readLock.Unlock()

	err := c.readIntoBuffer(true)
	if err != nil {
		return nil, err
	}

	msg := c.currentMessage
	c.currentMessage = nil
	return msg, nil
}

func (c *clientConnection) WriteMessage(b []byte) error {
	msg := Message{
		Meta:         MetaNone,
		OtherClient:  c.otherClient,
		ConnectionID: c.connID,
		Data:         b,
	}

	err := c.client.sendMessage(&msg)
	if err != nil {
		return err
	}

	return nil
}

func (c *clientConnection) OtherClient() string {
	return c.otherClient
}

// Client is the entity used to connect to Servers. Using a Client, you can
// send/receive new Connection requests.
type Client struct {
	name        string
	server      io.ReadWriteCloser
	serverRLock sync.Mutex
	serverWLock sync.Mutex

	newConnHandler NewConnectionHandler
	translator     MessageTranslator

	connections     map[string]*clientConnection
	connectionsLock sync.Mutex

	connNumber int64
	authed     bool
}

// NewClient creates a new Client instance with the given name + server
// communications
func NewClient(
	name string,
	server io.ReadWriteCloser,
	tm TranslatorMaker,
	ch NewConnectionHandler) *Client {

	return &Client{
		name:        name,
		server:      server,
		serverRLock: sync.Mutex{},
		serverWLock: sync.Mutex{},

		newConnHandler: ch,
		translator:     tm(server, server),

		connections:     make(map[string]*clientConnection),
		connectionsLock: sync.Mutex{},
	}
}

func (c *Client) sendMessage(m *Message) error {
	c.serverWLock.Lock()
	defer c.serverWLock.Unlock()

	return c.translator.WriteMessage(m)
}

func (c *Client) recvMessage() (*Message, error) {
	// Due to the design of Clients, recvMessage() should
	// only be called from Run() and Authenticate(), so locks
	// shouldn't be needed. However, because this code is in flux,
	// I'd rather not have races should the design change.
	// TODO: Potentially remove this.
	c.serverRLock.Lock()
	defer c.serverRLock.Unlock()

	return c.translator.ReadMessage()
}

// Authenticate allows the client to perform its initial handshake and
// authenticate itself with a Server. This should be called before
// Client.Run().
func (c *Client) Authenticate(password []byte) error {
	if c.authed {
		return errors.New(errStringMultipleAuths)
	}

	msg := Message{
		Meta:        MetaAuth,
		OtherClient: c.name,
		Data:        password,
	}

	err := c.sendMessage(&msg)
	if err != nil {
		return err
	}

	resp, err := c.recvMessage()
	if err != nil {
		return err
	}

	if resp.Meta != MetaAuthOk {
		return errors.New(string(resp.Data))
	}

	c.authed = true
	return nil
}

func (c *Client) nextConnID() string {
	nextInt := atomic.AddInt64(&c.connNumber, 1)

	// In personal use, I've never seen a connID exceed 64 bytes,
	// so there's no point in making extra garbage if it can be
	// (easily) avoided.
	var microoptimization [64]byte
	buf := microoptimization[:0]
	buf = append(buf, c.name...)
	buf = append(buf, ':')
	buf = strconv.AppendInt(buf, nextInt, 16)
	return string(buf)
}

// MakeConnection attempts to make a Connection with `otherClient` using the
// `proto` protocol. On success, you'll get a shiny new Connection instance
// and nil error. If anything goes wrong, you'll get an error.
//
// The Client will need to be authenticated and running when you call
// MakeConnection().
func (c *Client) MakeConnection(otherClient, proto string) (Connection, error) {
	if !c.authed {
		return nil, errors.New(errStringNotYetAuthed)
	}

	id := c.nextConnID()
	conn := newClientConnection(otherClient, id, c)

	c.connectionsLock.Lock()
	c.connections[id] = conn
	c.connectionsLock.Unlock()

	msg := &Message{
		Meta:         MetaConnSyn,
		OtherClient:  otherClient,
		ConnectionID: id,
		Data:         []byte(proto),
	}

	err := c.sendMessage(msg)
	if err != nil {
		return nil, err
	}

	data, err := conn.ReadMessage()
	if err != nil {
		return nil, err
	} else if data != nil {
		return nil, errors.New(string(data))
	}
	return conn, nil
}

func (c *Client) findAnyConnection(id string) (*clientConnection, bool) {
	c.connectionsLock.Lock()
	a, b := c.connections[id]
	c.connectionsLock.Unlock()
	return a, b
}

func (c *Client) findEstablishedConnection(id string) (*clientConnection, bool) {
	a, ok := c.findAnyConnection(id)
	if !ok || !a.isHandshakeComplete() {
		return nil, false
	}
	return a, true
}

func (c *Client) removeConnection(id string) (*clientConnection, bool) {
	c.connectionsLock.Lock()
	conn, ok := c.connections[id]
	if ok {
		delete(c.connections, id)
	}
	c.connectionsLock.Unlock()

	return conn, ok
}

// This was lifted out of handleMetaMessage because of its complexity
func (c *Client) metaHandleSyn(msg *Message, proto string) (resp bool, err error) {
	var conn *clientConnection
	var acceptCalled, callFinished bool

	accept := func() Connection {
		if callFinished {
			panic("You need to call accept() before IncomingConnection() returns")
		}
		if acceptCalled {
			panic("You may only call accept once.")
		}

		acceptCalled = true
		conn = newClientConnection(msg.OtherClient, msg.ConnectionID, c)

		// The whole reason that the complexity of having an accept() function
		// exists is so that we can guarantee that we send the ACK out before
		// the user gets their hands on the Connection object.
		c.connectionsLock.Lock()
		c.connections[msg.ConnectionID] = conn
		c.connectionsLock.Unlock()

		msg.Meta = MetaConnAck
		// Explicitly ignore errors sending the ACK because if there's a problem,
		// then this client will have to be shut down soon anyway, which will
		// cause the Connection we're returning to die.
		_ = c.sendMessage(msg)

		conn.noteHandshakeComplete()
		return conn
	}

	c.newConnHandler.IncomingConnection(proto, accept)
	callFinished = true
	if !acceptCalled {
		msg.Meta = MetaUnknownProto
		return true, nil
	}
	return false, nil
}

// This method is assumed to *only* be called by the goroutine that is
// processing incoming messages. It is not safe to call concurrently. If you
// want to do so, fix the parts annotated with !!! below.
//
// Note that this function has free reign to update msg however it sees fit.
// Make a deep copy if you don't want it updated.
//
// return (bool, error) is admittedly a bit weird. We'll update
// msg in-place with what we want to send back (if anything).
// Returns (?, err) on an unrecoverable error, (true, nil) if we
// want to respond, (false, nil) if we don't want to respond.
func (c *Client) handleMetaMessage(msg *Message) (resp bool, err error) {
	// In most cases, it's okay to bounce data back with our message, but it's
	// also undesirable to do so. So we nil out msg.Data and keep a snapshot of
	// what the data is, in case we explicitly want to send back the data that was
	// sent (or somehow use the data)
	msgData := msg.Data
	msg.Data = nil
	switch msg.Meta {
	case MetaNone:
		panic("Passed unmeta message to handleMetaMessage")
	case MetaNoSuchConnection, MetaConnClosed:
		conn, ok := c.removeConnection(msg.ConnectionID)
		if ok {
			conn.closeNoNotify()
		}
		return false, nil
	case MetaUnknownProto:
		cid := msg.ConnectionID
		conn, ok := c.removeConnection(cid)

		if ok {
			// !!! It's assumed that conn.isHandshakeComplete() will *not* change
			// throughout the execution of this if statement. Otherwise, this msg
			// will leak to the client.
			// (It's also expected that we'll never get MetaUnknownProto if our
			// handshake is complete, but the extra protection doesn't hurt)
			if !conn.isHandshakeComplete() {
				msg.Data = []byte(errStringUnknownProtocol)
				conn.putNewMessage(msg)
			}
			conn.closeNoNotify()
		}
		return false, nil
	case MetaConnSyn:
		proto := string(msgData)
		return c.metaHandleSyn(msg, proto)
	case MetaConnAck:
		id := msg.ConnectionID
		conn, ok := c.findAnyConnection(id)

		if !ok {
			msg.Meta = MetaNoSuchConnection
			return true, nil
		}

		conn.noteHandshakeComplete()
		conn.putNewMessage(msg)
		return false, nil
	case MetaClientClosed:
		otherClient := msg.OtherClient
		connections := c.connections
		c.connectionsLock.Lock()
		for k, conn := range c.connections {
			if conn.OtherClient() == otherClient {
				// If we want to change this to Close(), we need to move it out of this
				// loop. Otherwise, we'll hit a deadlock when Close() is trying to
				// notify the client of the closing (i.e. when it tries to acquire
				// connectionsLock)
				conn.closeNoNotify()
				delete(connections, k)
			}
		}
		c.connectionsLock.Unlock()
		return false, nil
	case MetaAuth, MetaAuthOk, MetaAuthFailure:
		msg.Meta = MetaWAT
		return true, nil
	default:
		s := fmt.Sprintf("Unknown meta message type passed into handleMetaMessage: %d", msg.Meta)
		return false, errors.New(s)
	}
}

// Close closes a Client's connection to the server, makes Client.Run() exit
// (eventually), and closes all Connection instances that this Client
// created.
func (c *Client) Close() error {
	err := c.server.Close()

	c.connectionsLock.Lock()
	connections := c.connections
	c.connections = make(map[string]*clientConnection)
	c.connectionsLock.Unlock()

	for _, conn := range connections {
		conn.closeNoNotify()
	}

	return err
}

// TODO: This is kind of an ugly hack, and can result
// in deadlock if we aren't careful, so we should probably
// try to send out the closedMsg in Run(). However, I don't
// see how to do that without making Run() more event-loopy
// (i.e. have it fire off a goroutine that hands us messages
// on a chan *Message, then have it select on that and channels
// for whatever else needs to be done).
//
// This may be a better design decision, so I'll swap to that in
// the future.
func (c *Client) notifyClosed(conn *clientConnection) error {
	_, ok := c.removeConnection(conn.connID)
	if !ok {
		return nil
	}

	closedMsg := &Message{
		Meta:         MetaConnClosed,
		OtherClient:  conn.otherClient,
		ConnectionID: conn.connID,
	}

	return c.sendMessage(closedMsg)
}

// Run is the main "event loop" for a Client. In it, the Client receives
// messages from the Server and dispatches them to Connections. You should
// call Authenticate() prior to calling Run().
func (c *Client) Run() error {
	if !c.authed {
		return errors.New(errStringNotYetAuthed)
	}

	defer c.server.Close()
	for {
		msg, err := c.recvMessage()
		if err != nil {
			return err
		}

		if msg.Meta == MetaNone {
			conn, ok := c.findEstablishedConnection(msg.ConnectionID)
			if !ok || !conn.putNewMessage(msg) {
				msg.Data = nil
				msg.Meta = MetaNoSuchConnection
				err = c.sendMessage(msg)
				if err != nil {
					return err
				}
			}
		} else {
			respond, err := c.handleMetaMessage(msg)
			if err != nil {
				return err
			}

			if respond {
				err = c.sendMessage(msg)
				if err != nil {
					return err
				}
			}
		}
	}
}
