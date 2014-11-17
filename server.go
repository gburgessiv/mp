package mp

import (
	"errors"
	"io"
	"net"
	"sync"
)

const (
	ProtoMaxSize = 32
)

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
	s.server.removeClient(s.name)
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

	s.name = name
	if !s.server.addClient(name, s) {
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

type Server struct {
	listener io.Closer
	auth     Authenticator

	translatorMaker TranslatorMaker

	clients     map[string]*serverClient
	clientsLock sync.Mutex

	closed bool
}

func NewServer(auth Authenticator, maker TranslatorMaker) *Server {
	return &Server{
		translatorMaker: maker,
		auth:            auth,

		clients: make(map[string]*serverClient),
	}
}

func (s *Server) addClient(name string, client *serverClient) bool {
	s.clientsLock.Lock()
	if s.closed {
		s.clientsLock.Unlock()
		return false
	}

	s.clients[name] = client
	s.clientsLock.Unlock()
	return true
}

func (s *Server) findClient(name string) (*serverClient, bool) {
	s.clientsLock.Lock()
	sc, ok := s.clients[name]
	s.clientsLock.Unlock()
	return sc, ok
}

func (s *Server) removeClient(name string) {
	s.clientsLock.Lock()
	delete(s.clients, name)
	s.clientsLock.Unlock()
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
