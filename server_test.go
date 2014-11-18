package mp

import (
	"io"
	"net"
	"sync"
	"testing"
)

type mockListener struct {
	conns     chan net.Conn
	closed    bool
	closeLock sync.Mutex
}

func newMockListener() *mockListener {
	return &mockListener{conns: make(chan net.Conn)}
}

func (m *mockListener) Accept() (net.Conn, error) {
	c, ok := <-m.conns
	if !ok {
		return nil, io.EOF
	}

	return c, nil
}

func (m *mockListener) Close() error {
	// Was warned about data races with m.closed. Decided to fix them forever.
	m.closeLock.Lock()
	defer m.closeLock.Unlock()

	if !m.closed {
		m.closed = true
		close(m.conns)
	}
	return nil
}

func (m *mockListener) Addr() net.Addr {
	panic("Can't get addr for mock listener")
}

var _ net.Listener = (*mockListener)(nil)
var _ MessageTranslator = (*gobTranslator)(nil)

func authAny(_ string, _ []byte) bool {
	return true
}

func TestServerAuthsNewConnections(t *testing.T) {
	listener := newMockListener()
	serv := NewServer(authAny, NewGobTranslator)
	defer serv.Close()
	go serv.Listen(listener)

	ourPipe, serverPipe := net.Pipe()
	defer ourPipe.Close()
	listener.conns <- serverPipe

	trans := NewGobTranslator(ourPipe, ourPipe)
	msg := &Message{
		Meta:        MetaAuth,
		OtherClient: "my-name",
		Data:        []byte("my-password"),
	}

	trans.WriteMessage(msg)
	msg, _ = trans.ReadMessage()
	if msg.Meta != MetaAuthOk {
		t.Error("Expected successful auth, got", msg.Meta)
	}
}

func TestServerClosesOnNoAuth(t *testing.T) {
	listener := newMockListener()
	serv := NewServer(authAny, NewGobTranslator)
	defer serv.Close()
	go serv.Listen(listener)

	ourPipe, serverPipe := net.Pipe()
	defer ourPipe.Close()
	listener.conns <- serverPipe

	trans := NewGobTranslator(ourPipe, ourPipe)
	msg := &Message{
		Meta:        MetaNone,
		OtherClient: "my-name",
		Data:        []byte("my-password"),
	}

	trans.WriteMessage(msg)
	msg, err := trans.ReadMessage()
	if err != io.EOF {
		t.Error("Expected EOF, got", err)
	}
}

func TestServerClosesOnRejectedAuth(t *testing.T) {
	listener := newMockListener()
	authNone := func(string, []byte) bool { return false }
	serv := NewServer(authNone, NewGobTranslator)
	defer serv.Close()
	go serv.Listen(listener)

	ourPipe, serverPipe := net.Pipe()
	defer ourPipe.Close()
	listener.conns <- serverPipe

	trans := NewGobTranslator(ourPipe, ourPipe)
	msg := &Message{
		Meta:        MetaNone,
		OtherClient: "my-name",
		Data:        []byte("my-password"),
	}

	trans.WriteMessage(msg)
	msg, err := trans.ReadMessage()
	if err != io.EOF {
		t.Error("Expected EOF, got", err)
	}
}

func makeAuthedServerClientPairs(clients ...string) (*Server, []MessageTranslator, []net.Conn) {
	listener := newMockListener()
	defer listener.Close()

	serv := NewServer(authAny, NewGobTranslator)
	go serv.Listen(listener)

	translators := make([]MessageTranslator, len(clients))
	conns := make([]net.Conn, len(clients))
	for i, c := range clients {
		ourSide, serverSide := net.Pipe()
		listener.conns <- serverSide

		ourTrans := NewGobTranslator(ourSide, ourSide)
		translators[i] = ourTrans
		conns[i] = ourSide

		ourTrans.WriteMessage(&Message{
			Meta:        MetaAuth,
			OtherClient: c,
			Data:        nil,
		})
	}

	for _, t := range translators {
		m, err := t.ReadMessage()
		if err != nil {
			panic(err)
		}

		if m.Meta != MetaAuthOk {
			panic("Expected AuthOK meta")
		}
	}

	return serv, translators, conns
}

func TestServerRoutesMessagesCorrectly(t *testing.T) {
	serv, trans, _ := makeAuthedServerClientPairs("c1", "c2", "c3")
	defer serv.Close()

	m1to3 := &Message{
		Meta:        MetaNone,
		OtherClient: "c3",
		Data:        []byte("m1to3"),
	}

	m3to1 := &Message{
		Meta:        MetaNone,
		OtherClient: "c1",
		Data:        []byte("m3to1"),
	}

	// Server's goroutine for c1 will block on writing to c3,
	// c3's will block on writing to c1. It's beautiful.
	trans[0].WriteMessage(m1to3)
	trans[2].WriteMessage(m3to1)

	recv3to1, _ := trans[0].ReadMessage()
	recv1to3, _ := trans[2].ReadMessage()

	if string(recv3to1.Data) != "m3to1" {
		t.Error("Didn't expect 3->1's data to be", string(recv3to1.Data))
	}

	if string(recv1to3.Data) != "m1to3" {
		t.Error("Didn't expect 1->3's data to be", string(recv3to1.Data))
	}

	// Name translation *should* have happened.
	if recv3to1.OtherClient != "c3" {
		t.Error("Didn't expect 3->1 other client to be", recv3to1.OtherClient)
	}

	if recv1to3.OtherClient != "c1" {
		t.Error("Didn't expect 1->3 other client to be", recv1to3.OtherClient)
	}
}
