// "Sketchy tests", i.e. targeted tests of edge conditions of the server.
// server_test.go is more for general "does it work well enough to pass"
// tests
package mp

import (
	"sync"
	"testing"
)

func TestServerBroadcastsToClientsWhenOneClientCloses(t *testing.T) {
	serv, trans, conns := makeAuthedServerClientPairs("c1", "c2", "c3")
	conns[2].Close()
	defer shutdownAuthedServerClientPairs(serv, conns)

	var wg sync.WaitGroup
	recvCloseNotification := func(n int) {
		defer wg.Done()
		msg, err := trans[n].ReadMessage()
		if err != nil {
			t.Fatal("Error from client:", err)
		}

		if msg.Meta != MetaClientClosed {
			t.Error("Expected message meta to be client closed, got", msg.Meta)
		}

		if msg.OtherClient != "c3" {
			t.Error("Expected other client to be c3, but was", msg.OtherClient)
		}
	}

	wg.Add(2)
	for i := 0; i < 2; i++ {
		go recvCloseNotification(i)
	}

	wg.Wait()
}

func TestServerClientReportsNoConnectionOnInvalidTarget(t *testing.T) {
	serv, trans, conns := makeAuthedServerClientPairs("c1", "c2", "c3")
	defer shutdownAuthedServerClientPairs(serv, conns)

	msg := &Message{
		OtherClient: "DNE",
	}

	trans[0].WriteMessage(msg)
	msg, err := trans[0].ReadMessage()
	if err != nil {
		t.Fatal(err)
	}

	if msg.Meta != MetaNoSuchConnection {
		t.Error("Expected no such connection complaint.")
	}
}
