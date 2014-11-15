package mp

import (
	"bytes"
	"io"
	"sync"
	"testing"
)

// -----------------------------------------------------------------------------
// Utilities for testing
// -----------------------------------------------------------------------------
type callbackTranslator struct {
	ReadCallback  func(io.Reader) (*Message, error)
	WriteCallback func(io.Writer, *Message) error
}

func (c *callbackTranslator) ReadFrom(r io.Reader) (*Message, error) {
	return c.ReadCallback(r)
}

func (c *callbackTranslator) WriteTo(w io.Writer, m *Message) error {
	return c.WriteCallback(w, m)
}

type channelTranslator struct {
	incoming, outgoing chan *Message
	closed             chan struct{}
}

func newChannelTranslator() *channelTranslator {
	return &channelTranslator{
		incoming: make(chan *Message),
		outgoing: make(chan *Message),
		closed:   make(chan struct{}),
	}
}

func (c *channelTranslator) Close() error {
	close(c.closed)
	return nil
}

func (c *channelTranslator) ReadFrom(io.Reader) (*Message, error) {
	select {
	case m := <-c.incoming:
		return m, nil
	case <-c.closed:
		return nil, io.EOF
	}
}

func (c *channelTranslator) WriteTo(_ io.Writer, m *Message) error {
	select {
	case c.outgoing <- m:
		return nil
	case <-c.closed:
		return io.EOF
	}
}

// Because all Client reads/writes go through the MessageTranslator we give it,
// most of the time we don't even need a ReadWriteCloser.
type nopRWC struct{}

func (*nopRWC) Read([]byte) (n int, err error) {
	return 0, io.EOF
}

func (*nopRWC) Write([]byte) (n int, err error) {
	return 0, io.EOF
}

func (*nopRWC) Close() error {
	return nil
}

type callbackConnectionHandler struct {
	Callback func(string, Connection) bool
}

func (h *callbackConnectionHandler) IncomingConnection(s string, c Connection) bool {
	return h.Callback(s, c)
}

// -----------------------------------------------------------------------------
// clientConnection testing
// -----------------------------------------------------------------------------
func TestClientConnectionWritesMessageDirectlyToClient(t *testing.T) {
	rwc := &nopRWC{}
	msg := []byte("Hello, World!")
	numWriteCalls := 0
	ct := callbackTranslator{
		ReadCallback: func(r io.Reader) (*Message, error) {
			t.Error("Read function was called.")
			return nil, io.EOF
		},
		WriteCallback: func(w io.Writer, m *Message) error {
			numWriteCalls++
			if w != rwc {
				t.Error("Didn't get expected nopRWC instance")
			}

			if &m.Data[0] != &msg[0] {
				t.Error("Didn't get expected message by reference.")
			}
			return nil
		},
	}

	client := NewClient("test-client", rwc, &ct, nil)
	conn := newClientConnection("other-client", "connId", client)
	n, err := conn.Write(msg)
	if err != nil {
		t.Error("Write failed:", err)
	} else if n != len(msg) {
		t.Error("Write reported", n, "<", len(msg), "bytes written")
	}

	err = conn.WriteMessage(msg)
	if err != nil {
		t.Error("WriteMessage failed:", err)
	}

	if numWriteCalls != 2 {
		t.Error("Expected 2 write calls, got", numWriteCalls)
	}
}

func TestClientConnectionIgnoresNilMessagesInRead(t *testing.T) {
	conn := newClientConnection("other-client", "connId", nil)
	msg1 := []byte("Hello")
	msg2 := []byte("World!")
	syncChan := make(chan struct{})
	go func() {
		msg := &Message{Data: msg1}
		conn.putNewMessage(msg)
		// We need the writes to not be smashed into one read, so we synchronize
		// with the test here.
		<-syncChan
		msg.Data = []byte{}
		conn.putNewMessage(msg)
		msg.Data = msg2
		conn.putNewMessage(msg)
	}()

	buf := make([]byte, 32)
	n, err := conn.Read(buf)
	if err != nil {
		t.Error("Read error:", err)
	} else if n != len(msg1) {
		t.Error("Reported buffer length of", n)
	} else if !bytes.Equal(buf[:n], msg1) {
		t.Error("Got bytes:", string(buf[:n]))
	}

	close(syncChan)
	n, err = conn.Read(buf)
	if err != nil {
		t.Error("Read error:", err)
	} else if n != len(msg2) {
		t.Error("Reported buffer length of", n)
	} else if !bytes.Equal(buf[:n], msg2) {
		t.Error("Got bytes:", string(msg2))
	}
}

func TestClientConnectionSpreadsBigMessagesAcrossManyReads(t *testing.T) {
	conn := newClientConnection("other-client", "connId", nil)
	// "Big messages"
	msg1 := []byte("Hello")
	go func() {
		msg := &Message{Data: msg1}
		conn.putNewMessage(msg)
		conn.Close()
	}()

	buf := make([]byte, 0, len(msg1))
	readBuf := make([]byte, 2)
	nReads := 0

	for {
		n, err := conn.Read(readBuf)
		if err == io.EOF {
			break
		} else if err != nil {
			t.Fatal("Error reading:", err)
		}

		buf = append(buf, readBuf[:n]...)
		nReads++
	}

	expNReads := len(msg1)/2 + len(msg1)%2
	if nReads != expNReads {
		t.Error("Expected", expNReads, "but got", nReads)
	}

	if !bytes.Equal(msg1, buf) {
		t.Error("Buffer wasn't expected:", buf)
	}
}

func TestClientConnectionReturnsOneMessagePerReadMessage(t *testing.T) {
	conn := newClientConnection("other-client", "connId", nil)
	msg1 := []byte("Hello")
	msg2 := []byte{}
	msg3 := []byte("World!")
	go func() {
		msg := &Message{Data: msg1}
		conn.putNewMessage(msg)
		msg.Data = msg2
		conn.putNewMessage(msg)
		msg.Data = msg3
		conn.putNewMessage(msg)
		conn.Close()
	}()

	msgs := [...][]byte{msg1, msg2, msg3}
	for i, m := range msgs {
		rmsg, err := conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(rmsg, m) {
			t.Errorf("%d message (%s) != expected (%s)", i, string(rmsg), string(m))
		}
	}
}

func TestClientConnectionWriteAndWriteMessageIncrementSeqNum(t *testing.T) {
	rwc := &nopRWC{}
	msg := []byte("Hello, World!")
	ct := newChannelTranslator()
	client := NewClient("test-client", rwc, ct, nil)
	conn := newClientConnection("other-client", "connId", client)
	go func() {
		conn.WriteMessage(msg)
		conn.Write(msg)
		conn.WriteMessage(msg)
		conn.Write(msg)
	}()

	defer ct.Close()
	msgA := <-ct.outgoing
	msgB := <-ct.outgoing
	msgC := <-ct.outgoing
	msgD := <-ct.outgoing

	if msgA.SeqNum+1 != msgB.SeqNum {
		t.Error("Sequence numbers don't reliably increment by 1")
	}

	if msgB.SeqNum+1 != msgC.SeqNum {
		t.Error("Sequence numbers don't reliably increment by 1")
	}

	if msgC.SeqNum+1 != msgD.SeqNum {
		t.Error("Sequence numbers don't reliably increment by 1")
	}
}

// -----------------------------------------------------------------------------
// Client
// -----------------------------------------------------------------------------
func TestClientSubmitsUnalteredUsernameAndPassword(t *testing.T) {
	rwc := &nopRWC{}
	clientName := "test-client"
	clientPass := []byte("test-password")
	readCalled := false
	writeCalled := false
	ct := callbackTranslator{
		ReadCallback: func(r io.Reader) (*Message, error) {
			readCalled = true
			if r != rwc {
				t.Error("Didn't get expected nopRWC instance")
			}
			msg := &Message{Meta: MetaAuthOk}
			return msg, nil
		},
		WriteCallback: func(w io.Writer, m *Message) error {
			writeCalled = true
			if w != rwc {
				t.Error("Didn't get expected nopRWC instance")
			}
			if m.Meta != MetaAuth {
				t.Error("Auth message type wasn't MetaAuth")
			}
			if m.OtherClient != clientName {
				t.Error("Didn't submit proper client name. Submitted:", m.OtherClient)
			}
			if !bytes.Equal(m.Data, clientPass) {
				t.Error("Didn't submit requested password. Submitted:", string(m.Data))
			}
			return nil
		},
	}

	client := NewClient(clientName, rwc, &ct, nil)
	err := client.Authenticate(clientPass)
	if err != nil {
		t.Error("Got error authenticating:", err)
	}

	if !readCalled || !writeCalled {
		t.Error("Authenticate didn't call read/write")
	}
}

func TestClientSendsSynAckOnNewConnectionRequest(t *testing.T) {
	rwc := &nopRWC{}
	otherClient := "other-client"
	proto := "proto1"
	clientName := "test-client"
	ct := newChannelTranslator()

	client := NewClient(clientName, rwc, ct, nil)
	client.authed = true

	// I want the goroutines to exit cleanly before the test ends.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		err := client.Run()
		if err != io.EOF {
			t.Error("Client died with", err)
		}
		wg.Done()
	}()

	// Main test routine routes messages, so we leave making the connection to
	// other goroutines
	go func() {
		_, err := client.MakeConnection(otherClient, proto)
		if err != nil {
			t.Fatal(err)
		}
		wg.Done()
	}()

	defer func() {
		ct.Close()
		wg.Wait()
	}()

	msg := <-ct.outgoing
	if msg.Meta != MetaConnSyn {
		t.Error("Expected Syn meta type in message, got", msg.Meta)
	}

	if mData := string(msg.Data); mData != proto {
		t.Error("Unexpected message data:", mData)
	}

	if msg.OtherClient != otherClient {
		t.Error("Other client was unexpectedly", msg.OtherClient)
	}

	msg.Meta = MetaConnAck
	ct.incoming <- msg
}

func TestClientSendsNoSuchConnectionOnAckOrRegularMessage(t *testing.T) {
	rwc := &nopRWC{}
	clientName := "test-client"
	ct := newChannelTranslator()

	client := NewClient(clientName, rwc, ct, nil)
	client.authed = true

	// I want the goroutines to exit cleanly before the test ends.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		err := client.Run()
		if err != io.EOF {
			t.Error("Client died with", err)
		}
		wg.Done()
	}()

	defer func() {
		ct.Close()
		wg.Wait()
	}()

	msg := &Message{
		Meta:         MetaConnAck,
		ConnectionId: "does-not-exist",
		OtherClient:  "no-client",
		Data:         []byte("Noproto"),
	}

	ct.incoming <- msg
	resp := <-ct.outgoing
	if resp.Meta != MetaNoSuchConnection {
		t.Error("Expected meta to be MetaNoSuchConnection, was", resp.Meta)
	}

	msg.Meta = MetaNone
	ct.incoming <- msg
	resp = <-ct.outgoing
	if resp.Meta != MetaNoSuchConnection {
		t.Error("Expected meta to be MetaNoSuchConnection, was", resp.Meta)
	}
}

func TestClientSendsWatWhenSentAuthMessage(t *testing.T) {
	rwc := &nopRWC{}
	clientName := "test-client"
	ct := newChannelTranslator()

	client := NewClient(clientName, rwc, ct, nil)
	client.authed = true

	// I want the goroutines to exit cleanly before the test ends.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		err := client.Run()
		if err != io.EOF {
			t.Error("Client died with", err)
		}
		wg.Done()
	}()

	defer func() {
		ct.Close()
		wg.Wait()
	}()

	msg := &Message{
		ConnectionId: "does-not-exist",
		OtherClient:  "no-client",
		Data:         []byte("Noproto"),
	}

	metas := [...]MetaType{MetaAuth, MetaAuthOk, MetaAuthFailure}
	for _, m := range metas {
		msg.Meta = m
		ct.incoming <- msg
		resp := <-ct.outgoing
		if resp.Meta != MetaWAT {
			t.Error("Expected WAT response to", m, "got", resp.Meta)
		}
	}
}

func TestClientObeysSynHandlerDecisions(t *testing.T) {
	rwc := &nopRWC{}
	clientName := "test-client"
	inputProto := "input-proto"
	ct := newChannelTranslator()
	ch := &callbackConnectionHandler{
		Callback: func(proto string, _ Connection) bool {
			return proto == inputProto
		},
	}

	client := NewClient(clientName, rwc, ct, ch)
	client.authed = true

	// I want the goroutines to exit cleanly before the test ends.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		err := client.Run()
		if err != io.EOF {
			t.Error("Client died with", err)
		}
		wg.Done()
	}()

	msg := &Message{
		Meta:         MetaConnSyn,
		ConnectionId: "test-connid",
		OtherClient:  "other",
		Data:         []byte(inputProto),
	}

	ct.incoming <- msg
	ack := <-ct.outgoing
	if ack.Meta != MetaConnAck {
		t.Error("Meta is wrong:", ack.Meta)
	}

	// ack == msg, so we need to reset meta.
	msg.Meta = MetaConnSyn
	msg.Data = append([]byte(inputProto), '!')

	ct.incoming <- msg
	ack = <-ct.outgoing
	if ack.Meta != MetaUnknownProto {
		t.Error("Meta is wrong:", ack.Meta)
	}
}
