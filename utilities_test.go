package mp

import (
	"testing"
)

func TestMappedConnectionHandlerFunctionsWithNoMappings(t *testing.T) {
	ch := NewMappedConnectionHandler()
	ok := ch.IncomingConnection("does-not-exist", nil)
	if ok {
		t.Fatal("???")
	}
}

func TestMappedConnectionHandlerRunsInGoroutines(t *testing.T) {
	incoming := make(chan int)
	outgoing := make(chan int)
	ch := NewMappedConnectionHandler()
	ch.AddMapping("proto", func(Connection) {
		if n := <-incoming; n != 1 {
			t.Error("Expected incoming to give us 1, was", n)
		}
		outgoing <- 2
	})

	ok := ch.IncomingConnection("proto", nil)
	if !ok {
		t.Fatal("Incoming connection failed")
	}

	incoming <- 1
	if n := <-outgoing; n != 2 {
		t.Error("Expected 2 from outgoing, was", n)
	}
}

func TestMappedConnectionHandlerReturnsExpected(t *testing.T) {
	ch := NewMappedConnectionHandler()
	ch.AddMapping("exists", func(Connection) {})
	ok := ch.IncomingConnection("exists", nil)
	if !ok {
		t.Error("Expected IncomingConnection to exist")
	}

	ok = ch.IncomingConnection("DNE", nil)
	if ok {
		t.Error("Expected IncomingConnection to not exist")
	}
}
