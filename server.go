package mp

import (
	"errors"
	"io"
	"net"
	"strings"
	"sync"
)

func isClientNameValid(name string) bool {
	return !strings.ContainsRune(name, ':')
}

type serverClient struct {
	conn       io.ReadWriteCloser
	name       string
	server     *Server
	translator MessageTranslator
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
		err = errors.New("Expected MetaAuth as first message")
		return
	}

	name = msg.OtherClient
	secret := msg.Data
	ok := s.server.auth(name, secret)
	if !ok {
		err = errors.New("Authentication didn't recognize name+secret pair")
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
		if !ok {
			msg.Meta = MetaNoSuchConnection
			err = s.translator.WriteMessage(msg)
			if err != nil {
				return err
			}
			continue
		}

		msg.OtherClient = s.name
		err = other.WriteMessage(msg)
		if err != nil {
			msg.Meta = MetaNoSuchConnection
			err = s.translator.WriteMessage(msg)
			if err != nil {
				return err
			}
		}
	}
}

func (s *serverClient) AuthenticateAndRun() error {
	defer s.Close()

	name, err := s.Authenticate()
	if err != nil {
		s.conn.Close()
		return err
	}

	if !isClientNameValid(name) {
		s.conn.Close()
		return errors.New("Client name contained invalid characters")
	}

	s.name = name
	if !s.server.addClient(s) {
		s.conn.Close()
		return errors.New("Error adding new client")
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
	return s.translator.WriteMessage(msg)
}

type Authenticator func(name string, secret []byte) bool
type TranslatorMaker func(io.Reader, io.Writer) MessageTranslator

// A server!
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
	for {
		s.clientsLock.Lock()
		if s.closed {
			return false
		}

		if running, ok := s.clients[name]; ok {
			s.clientsLock.Unlock()
			running.Close()
			continue
		}

		if closing, ok := s.closingClients[name]; ok {
			s.clientsLock.Unlock()
			closing.Wait()
			continue
		}

		s.clients[name] = client
		s.clientsLock.Unlock()
		return true
	}
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
}
