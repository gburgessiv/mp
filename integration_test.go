package mp

import (
	"io"
	"net"
	"testing"
)

func closeServerAndClients(s *Server, cs []*Client) {
	for _, c := range cs {
		c.Close()
	}
	s.Close()
}

func serverWithClients(connh NewConnectionHandler, names ...string) (*Server, []*Client) {
	listener := newMockListener()
	defer listener.Close()

	clients := make([]*Client, len(names))
	serv := NewServer(authAny, NewGobTranslator)
	go serv.Listen(listener)

	for i, n := range names {
		ours, theirs := net.Pipe()
		listener.conns <- theirs

		client := NewClient(n, ours, NewGobTranslator, connh)
		clients[i] = client
		err := client.Authenticate(nil)
		if err != nil {
			panic(err)
		}

		go func() {
			if err := client.Run(); err != io.EOF && err != io.ErrClosedPipe {
				panic(err)
			}
		}()
	}

	return serv, clients
}

func TestClientsCanCommunicateThroughTheServer(t *testing.T) {
	const sendMsg = "Hello, World!"
	// This channel needs to be buffered so we're not
	// blocking on ourself.
	c2Chan := make(chan Connection, 1)
	handler := &callbackConnectionHandler{
		Callback: func(s string, conn Connection) bool {
			c2Chan <- conn
			return true
		},
	}

	server, clients := serverWithClients(handler, "c1", "c2")
	defer closeServerAndClients(server, clients)

	conn, err := clients[0].MakeConnection("c2", "foo")
	if err != nil {
		t.Fatal(err)
	}

	conn2 := <-c2Chan

	conn.Write([]byte(sendMsg))
	msg, err := conn2.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}

	if string(msg) != sendMsg {
		t.Errorf("Unexpectedly got %s as the message", string(msg))
	}
}

func TestNoSuchProtocolReportedProperly(t *testing.T) {
	handlerCalled := false
	handler := &callbackConnectionHandler{
		Callback: func(s string, conn Connection) bool {
			handlerCalled = false
			return false
		},
	}

	server, clients := serverWithClients(handler, "c1", "c2")
	defer closeServerAndClients(server, clients)

	_, err := clients[0].MakeConnection("c2", "foo")
	if err == nil || err.Error() != ErrStringUnknownProtocol {
		t.Fatal(err)
	}
}
