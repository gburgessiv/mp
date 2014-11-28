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
	ReadCallback  func() (*Message, error)
	WriteCallback func(*Message) error
}

func (c *callbackTranslator) ReadMessage() (*Message, error) {
	return c.ReadCallback()
}

func (c *callbackTranslator) WriteMessage(m *Message) error {
	return c.WriteCallback(m)
}

type channelTranslator struct {
	incoming, outgoing chan *Message
	closed             chan struct{}
}

func newChannelTranslator() *channelTranslator {
	// Incoming can't be buffered because race -- take the following
	// code:
	//   ct := newChannelTranslator()
	//   go readFromCTIncoming(ct)
	//   ct.incoming <- nil
	//   ct.Close()
	//
	// ...If ct.incoming doesn't block before nil is passed off, then
	// it's unspecified whether or not the nil will ever be received.
	// (In my implementation of Go [v1.3], nil is never received).
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

func (c *channelTranslator) ReadMessage() (*Message, error) {
	select {
	case m := <-c.incoming:
		return m, nil
	case <-c.closed:
		return nil, io.EOF
	}
}

func (c *channelTranslator) WriteMessage(m *Message) error {
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

func singletonTranslator(t MessageTranslator) TranslatorMaker {
	return func(io.Reader, io.Writer) MessageTranslator {
		return t
	}
}

// -----------------------------------------------------------------------------
// clientConnection testing
// -----------------------------------------------------------------------------
func TestClientConnectionWritesMessageDirectlyToClient(t *testing.T) {
	msg := []byte("Hello, World!")
	numWriteCalls := 0
	ct := callbackTranslator{
		ReadCallback: func() (*Message, error) {
			t.Error("Read function was called.")
			return nil, io.EOF
		},
		WriteCallback: func(m *Message) error {
			numWriteCalls++
			if &m.Data[0] != &msg[0] {
				t.Error("Didn't get expected message by reference.")
			}
			return nil
		},
	}

	client := NewClient("test-client", &nopRWC{}, singletonTranslator(&ct), nil)
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

// -----------------------------------------------------------------------------
// Client
// -----------------------------------------------------------------------------
func TestClientSubmitsUnalteredUsernameAndPassword(t *testing.T) {
	const clientName = "test-client"
	clientPass := []byte("test-password")
	readCalled := false
	writeCalled := false
	ct := callbackTranslator{
		ReadCallback: func() (*Message, error) {
			readCalled = true
			msg := &Message{Meta: MetaAuthOk}
			return msg, nil
		},
		WriteCallback: func(m *Message) error {
			writeCalled = true
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

	client := NewClient(clientName, &nopRWC{}, singletonTranslator(&ct), nil)
	err := client.Authenticate(clientPass)
	if err != nil {
		t.Error("Got error authenticating:", err)
	}

	if !readCalled || !writeCalled {
		t.Error("Authenticate didn't call read/write")
	}
}

func TestClientSendsSynAckOnNewConnectionRequest(t *testing.T) {
	const (
		otherClient = "other-client"
		clientName  = "test-client"
		proto       = "proto1"
	)
	var wg sync.WaitGroup
	ct := newChannelTranslator()

	defer func() {
		ct.Close()
		wg.Wait()
	}()

	client := NewClient(clientName, &nopRWC{}, singletonTranslator(ct), nil)
	client.authed = true

	// I want the goroutines to exit cleanly before the test ends.
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
	const clientName = "test-client"
	rwc := &nopRWC{}
	ct := newChannelTranslator()

	client := NewClient(clientName, rwc, singletonTranslator(ct), nil)
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
	const clientName = "test-client"
	var wg sync.WaitGroup
	ct := newChannelTranslator()

	defer func() {
		ct.Close()
		wg.Wait()
	}()

	client := NewClient(clientName, &nopRWC{}, singletonTranslator(ct), nil)
	client.authed = true

	// I want the goroutines to exit cleanly before the test ends.
	wg.Add(1)
	go func() {
		err := client.Run()
		if err != io.EOF {
			t.Error("Client died with", err)
		}
		wg.Done()
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
	const (
		clientName = "test-client"
		inputProto = "input-proto"
	)

	ch := &callbackConnectionHandler{
		Callback: func(proto string, _ Connection) bool {
			return proto == inputProto
		},
	}

	ct := newChannelTranslator()
	defer ct.Close()

	client := NewClient(clientName, &nopRWC{}, singletonTranslator(ct), ch)
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

func TestClientSendsCloseNotificationOnConnectionClose(t *testing.T) {
	rwc := &nopRWC{}
	clientName := "test-client"
	inputProto := "input-proto"
	ct := newChannelTranslator()
	defer ct.Close()

	var clientConn Connection
	ch := &callbackConnectionHandler{
		Callback: func(_ string, c Connection) bool {
			clientConn = c
			return true
		},
	}

	client := NewClient(clientName, rwc, singletonTranslator(ct), ch)
	client.authed = true

	// I want the goroutines to exit cleanly before the test ends.
	go func() {
		err := client.Run()
		if err != io.EOF {
			t.Error("Client died with", err)
		}
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

	// clientConn should now be set. We run this in a goroutine because
	// sendMessage() is done synchronously, and channelTranslator is
	// synchronous.
	go clientConn.Close()

	out := <-ct.outgoing
	if out.Meta != MetaConnClosed {
		t.Error("Meta is wrong:", ack.Meta)
	}
}

func TestClientClosesConnectionIfOtherSideClosed(t *testing.T) {
	rwc := &nopRWC{}
	clientName := "test-client"
	inputProto := "input-proto"
	otherClient := "other-client"
	ct := newChannelTranslator()
	var wg sync.WaitGroup
	defer func() {
		ct.Close()
		wg.Wait()
	}()

	client := NewClient(clientName, rwc, singletonTranslator(ct), nil)
	client.authed = true

	wg.Add(2)
	go func() {
		defer wg.Done()
		err := client.Run()
		if err != io.EOF {
			t.Error("Client died with", err)
		}
	}()

	// "Main" goroutine. I'm just juggling messages and things in the
	// test after this runs
	go func() {
		defer wg.Done()
		conn, err := client.MakeConnection(otherClient, inputProto)
		if err != nil {
			t.Fatal("Error making connection:", err)
		}

		_, err = conn.ReadMessage()
		if err != io.EOF {
			t.Error(err)
		}
	}()

	syn := <-ct.outgoing
	connId := syn.ConnectionId

	syn.Meta = MetaConnAck
	ct.incoming <- syn

	closedMsg := &Message{
		Meta:         MetaConnClosed,
		ConnectionId: connId,
		OtherClient:  otherClient,
	}

	ct.incoming <- closedMsg
}

func TestClientClosesConnectionIfOtherClientClosed(t *testing.T) {
	const (
		clientName  = "test-client"
		otherClient = "other-client"
	)
	ct := newChannelTranslator()
	var wg sync.WaitGroup
	defer func() {
		ct.Close()
		wg.Wait()
	}()

	client := NewClient(clientName, &nopRWC{}, singletonTranslator(ct), nil)
	client.authed = true

	wg.Add(3)
	go func() {
		defer wg.Done()
		err := client.Run()
		if err != io.EOF {
			t.Error("Client died with", err)
		}
	}()

	// "Main" goroutine 1 -- half-establishes a connection
	go func() {
		defer wg.Done()
		_, err := client.MakeConnection(otherClient, "half-open")
		if err == nil {
			t.Fatal("No error making a connection")
		}
	}()

	// "Main" goroutine 2 -- fully establishes a connection
	go func() {
		defer wg.Done()
		conn, err := client.MakeConnection(otherClient, "full-open")
		if err != nil {
			t.Fatal("Error making connection:", err)
		}

		_, err = conn.ReadMessage()
		if err != io.EOF {
			t.Error(err)
		}
	}()

	for i := 0; i < 2; i++ {
		syn := <-ct.outgoing
		if string(syn.Data) != "full-open" {
			continue
		}

		syn.Meta = MetaConnAck
		ct.incoming <- syn
	}

	closedMsg := &Message{
		Meta:        MetaClientClosed,
		OtherClient: otherClient,
	}

	ct.incoming <- closedMsg
}
