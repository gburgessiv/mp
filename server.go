package mp

import (
	"errors"
	"io"
	"net"
	"strings"
	"sync"
)

const (
	errStringAuthDenied = "authentication didn't recognize name+secret pair"
	errStringBadName    = "client name is invalid"
)

func isClientNameValid(name string) bool {
	return len(name) > 0 && !strings.ContainsRune(name, ':')
}

type serverClient struct {
	conn       io.ReadWriteCloser
	name       string
	server     *Server
	translator MessageTranslator
	writeLock  sync.Mutex
	authed     bool
}

func newUnauthedServerClient(c io.ReadWriteCloser, s *Server, t MessageTranslator) *serverClient {
	return &serverClient{
		conn:       c,
		server:     s,
		translator: t,
	}
}

func (s *serverClient) Authenticate() (name string, err error) {
	msg, err := s.translator.ReadMessage()
	if err != nil {
		return
	}

	if msg.Meta != MetaAuth {
		err = errors.New("expected MetaAuth as first message")
		return
	}

	name = msg.OtherClient
	if !isClientNameValid(name) {
		err = errors.New(errStringBadName)
		return
	}

	secret := msg.Data
	ok := s.server.auth(name, secret)
	if !ok {
		err = errors.New(errStringAuthDenied)
		return
	}

	s.authed = true
	return
}

func (s *serverClient) Close() error {
	s.conn.Close()
	s.server.removeClientAndNotify(s)
	return nil
}

func (s *serverClient) Run() error {
	if !s.authed {
		panic("Trying to run an unauthed serverClient")
	}

	server := s.server
	for {
		msg, err := s.translator.ReadMessage()
		if err != nil {
			return err
		}

		other, ok := server.findClient(msg.OtherClient)
		if ok {
			msg.OtherClient = s.name
			err = other.WriteMessage(msg)
			ok = err == nil
		}

		// Please note that `ok` is set above. I just didn't want to duplicate
		// error handling code.
		if !ok {
			msg.Meta = MetaNoSuchConnection
			err = s.translator.WriteMessage(msg)
			if err != nil {
				return err
			}
		}
	}
	// Compat with earlier versions of Go
	return errors.New("Internal error: Run exited prematurely")
}

func (s *serverClient) AuthenticateAndRun() error {
	defer s.Close()

	name, err := s.Authenticate()
	if err != nil {
		s.conn.Close()
		return err
	}

	s.name = name
	if !s.server.addClient(s) {
		s.conn.Close()
		return errors.New("error adding new client")
	}

	err = s.WriteMessage(&Message{
		Meta:        MetaAuthOk,
		OtherClient: s.name,
	})

	if err != nil {
		return err
	}

	return s.Run()
}

func (s *serverClient) WriteMessage(msg *Message) error {
	s.writeLock.Lock()
	defer s.writeLock.Unlock()
	return s.translator.WriteMessage(msg)
}

// Authenticator is a function that returns whether or not the given name/secret
// pair is valid, and that the sender of it should be allowed to connect to the
// server.
//
// `secret` may be nil.
type Authenticator func(name string, secret []byte) bool

// TranslatorMaker is a function that, given a Reader and Writer, returns a
// MessageTranslator that reads from and writes to the given Reader and Writer.
type TranslatorMaker func(io.Reader, io.Writer) MessageTranslator

// Server is an implementation of a MessagePassing Server.
// (Golint made me do this)
type Server struct {
	listener io.Closer
	auth     Authenticator

	translatorMaker TranslatorMaker

	clients        map[string]*serverClient
	closingClients map[string]*sync.WaitGroup // see [1]
	clientsLock    sync.Mutex

	closed bool
}

// [1] -- There's a race when one client disconnects and another connects
// immediately (with the same name). The easiest way to solve this is to wait
// for the client closed message to be fully broadcast before the new client
// can receive connections.

// NewServer creates a new Server instance that uses the given Authenticator
// and TranslatorMaker
func NewServer(auth Authenticator, maker TranslatorMaker) *Server {
	return &Server{
		translatorMaker: maker,
		auth:            auth,

		clients:        make(map[string]*serverClient),
		closingClients: make(map[string]*sync.WaitGroup),
	}
}

func (s *Server) addClient(client *serverClient) bool {
	// Adding is harder than it needs to be; 3 cases need to be considered:
	// Client in clients queue? Close that + try again.
	// Client in closingClients queue? Wait for that to finish closing + try again.
	// Else, just add it.
	// Better yet, all of this needs to happen as one single atomic operation. :)
	name := client.name
	ok := true

	// Can't defer unlock because the lock needs to be released for running.Close() and
	// closing.Wait(). If either of those panics, the clientsLock may be Unlocked when it was
	// already Unlocked.
	s.clientsLock.Lock()
	for {
		if s.closed {
			ok = false
			break
		}

		if running, ok := s.clients[name]; ok {
			s.clientsLock.Unlock()
			running.Close()
			s.clientsLock.Lock()
			continue
		}

		if closing, ok := s.closingClients[name]; ok {
			s.clientsLock.Unlock()
			closing.Wait()
			s.clientsLock.Lock()
			continue
		}

		s.clients[name] = client
		break
	}
	s.clientsLock.Unlock()

	return ok
}

func (s *Server) findClient(name string) (*serverClient, bool) {
	s.clientsLock.Lock()
	sc, ok := s.clients[name]
	s.clientsLock.Unlock()
	return sc, ok
}

// Puts a client in the 'closing' queue. The given WaitGroup needs
// Done to be called once on it.
func (s *Server) queueClientForClose(client *serverClient) (*sync.WaitGroup, bool) {
	s.clientsLock.Lock()
	defer s.clientsLock.Unlock()

	name := client.name

	c, ok := s.clients[name]
	if !ok || c != client {
		return nil, false
	}

	wg := new(sync.WaitGroup)
	wg.Add(1)
	if _, ok := s.closingClients[name]; ok {
		panic("Somehow we have two clients with the same name closing simultaneously")
	}
	s.closingClients[name] = wg
	return wg, true
}

func (s *Server) removeClientFromClosingMap(c *serverClient) {
	name := c.name

	s.clientsLock.Lock()
	wg, ok := s.closingClients[name]
	if ok {
		delete(s.closingClients, name)
	}
	s.clientsLock.Unlock()

	if ok {
		wg.Done()
	}
}

func (s *Server) removeClientAndNotify(c *serverClient) {
	_, ok := s.queueClientForClose(c)
	if !ok {
		return
	}

	defer s.removeClientFromClosingMap(c)
	msg := Message{
		Meta:        MetaClientClosed,
		OtherClient: c.name,
	}

	s.broadcastMessage(&msg)
}

// Gets a snapshot of all of the clients that are currently connected to
// the server.
func (s *Server) snapshotClients() []*serverClient {
	s.clientsLock.Lock()
	defer s.clientsLock.Unlock()

	snapshot := make([]*serverClient, len(s.clients))
	i := 0
	for _, c := range s.clients {
		snapshot[i] = c
		i++
	}

	return snapshot
}

// This will broadcast a message to all of the clients that are currently
// connected to the server. If a client connects in the middle of the broadcast,
// then it will *NOT* receive the message.
//
// Additionally, if the server has been closed, then the message will just be
// dropped, because there are no clients to send to.
func (s *Server) broadcastMessage(m *Message) {
	clients := s.snapshotClients()
	for _, c := range clients {
		c.WriteMessage(m)
	}
}

// Close closes and shuts down the server. It will cause Listen() on the
// current instance to exit, and will sever the server's connection to all
// Clients.
func (s *Server) Close() {
	s.listener.Close()

	s.clientsLock.Lock()
	clients := s.clients
	s.clients = make(map[string]*serverClient)
	s.closed = true
	s.clientsLock.Unlock()

	for _, client := range clients {
		client.Close()
	}
}

// Listen listens for new Client connections to the Server. When Listen exits,
// the Server will continue to serve Clients that are already connected.
func (s *Server) Listen(l net.Listener) error {
	s.listener = l

	// Close the listener, not the server. The server can operate perfectly fine
	// without the ability to accept new clients
	defer l.Close()

	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}

		nc := newUnauthedServerClient(conn, s, s.translatorMaker(conn, conn))
		go nc.AuthenticateAndRun()
	}

	// compat with Go 1.0
	panic(unreachableCode)
}
