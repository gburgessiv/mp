package mp

import (
	"bytes"
	"io"
	"sync"
	"sync/atomic"
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
	isClosed           uint32
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
	if atomic.CompareAndSwapUint32(&c.isClosed, 0, 1) {
		close(c.closed)
	}
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
	Callback func(string, func() Connection)
}

func (h *callbackConnectionHandler) IncomingConnection(s string, c func() Connection) {
	h.Callback(s, c)
}

func singletonTranslator(t MessageTranslator) TranslatorMaker {
	return func(io.Reader, io.Writer) MessageTranslator {
		return t
	}
}

// -----------------------------------------------------------------------------
// clientConnection testing
// -----------------------------------------------------------------------------

var _ Connection = (*clientConnection)(nil)

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
	conn := newClientConnection("other-client", "connID", client)
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

// No, I'm not kidding.
func TestClientConnectionOtherClientWorks(t *testing.T) {
	conn := newClientConnection("foo", "id", nil)
	if conn.OtherClient() != "foo" {
		t.Error("How did it return", conn.OtherClient())
	}
}

func TestClientConnectionIgnoresNilMessagesInRead(t *testing.T) {
	conn := newClientConnection("other-client", "connID", nil)
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
	conn := newClientConnection("other-client", "connID", nil)
	// "Big messages"
	msg1 := []byte("Hello")
	go func() {
		msg := &Message{Data: msg1}
		conn.putNewMessage(msg)
		conn.Close()
	}()

	buf := make([]byte, 0, len(msg1))
	var readBuf [2]byte
	nReads := 0
	for {
		n, err := conn.Read(readBuf[:])
		buf = append(buf, readBuf[:n]...)
		nReads++

		if err == io.EOF {
			break
		} else if err != nil {
			t.Fatal("Error reading:", err)
		}
	}

	expNReads := len(msg1)/2 + len(msg1)%2
	if nReads != expNReads {
		t.Error("Expected", expNReads, "reads but got", nReads)
	}

	if !bytes.Equal(msg1, buf) {
		t.Error("Buffer wasn't expected:", buf)
	}
}

func TestClientConnectionReturnsOneMessagePerReadMessage(t *testing.T) {
	conn := newClientConnection("other-client", "connID", nil)
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

func mockBoundClientConnection() (*channelTranslator, *clientConnection) {
	ct := newChannelTranslator()
	client := NewClient("client-connection-client", &nopRWC{}, singletonTranslator(ct), nil)
	conn := newClientConnection("client-connection", "test-id", client)
	return ct, conn
}

func TestClientConnectionHandlesWriteMessageFailureGracefully(t *testing.T) {
	ct, conn := mockBoundClientConnection()

	ct.Close()
	err := conn.WriteMessage([]byte("Hi!"))
	if err != io.EOF {
		t.Error("Expected EOF, got", err)
	}

	_, err = conn.Write([]byte("Hi!"))
	if err != io.EOF {
		t.Error("Expected EOF, got", err)
	}
}

func TestClientConnectionHandlesReadMessageFailureGracefully(t *testing.T) {
	conn := newClientConnection("client-connection", "test-id", nil)
	conn.Close()
	_, err := conn.ReadMessage()
	if err != io.EOF {
		t.Error("Expected EOF, got", err)
	}

	var bs [1]byte
	_, err = conn.Read(bs[:])
	if err != io.EOF {
		t.Error("Expected EOF, got", err)
	}
}

func TestClientConnectionReadReportsProperAmount(t *testing.T) {
	const InputBufferLen = 3
	conn := newClientConnection("client-connection", "test-id", nil)
	defer conn.Close()

	// Conveniently InputBufferLen+1 and InputBufferLen*2, respectively
	messages := [...]string{"Hi!!", "Hello!"}

	for _, message := range messages {
		msg := &Message{Data: []byte(message)}
		go conn.putNewMessage(msg)

		var inBuf [InputBufferLen]byte
		n, err := conn.Read(inBuf[:])
		if err != nil {
			t.Fatal(err)
		}

		if n != len(inBuf) {
			t.Error("Got less bytes than expected.")
		} else if exp := msg.Data[:n]; !bytes.Equal(inBuf[:], exp) {
			t.Error("Data wasn't what eas expected. Got", inBuf, "-- expected", exp)
		}

		remData := msg.Data[n:]
		n, err = conn.Read(inBuf[:])
		if err != nil {
			t.Fatal(err)
		}

		if n != len(remData) {
			t.Errorf("Got %d bytes, expected %d.", n, len(remData))
		} else if got := inBuf[:n]; !bytes.Equal(remData, got) {
			t.Errorf("Got bytes %v, expected %v", got, remData)
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

func makeAuthedClient(
	t *testing.T,
	handler NewConnectionHandler,
) (*Client, *channelTranslator, *sync.WaitGroup, func()) {

	wg := new(sync.WaitGroup)
	ct := newChannelTranslator()
	client := NewClient("client-name", &nopRWC{}, singletonTranslator(ct), handler)
	client.authed = true

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := client.Run()
		if err != io.EOF {
			t.Error("Client died with", err)
		}
	}()

	return client, ct, wg, func() {
		ct.Close()
		client.Close()
		wg.Wait()
	}
}

func TestClientSendsSynAckOnNewConnectionRequest(t *testing.T) {
	const (
		otherClient = "other-client"
		proto       = "proto1"
	)

	client, ct, _, shutdown := makeAuthedClient(t, nil)
	defer shutdown()

	// Main test routine routes messages, so we leave making the connection to
	// other goroutines
	connectionMade := make(chan struct{})
	go func() {
		_, err := client.MakeConnection(otherClient, proto)
		if err != nil {
			t.Fatal(err)
		}
		close(connectionMade)
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
	<-connectionMade
}

func TestClientSendsNoSuchConnectionOnAckOrRegularMessage(t *testing.T) {
	_, ct, _, shutdown := makeAuthedClient(t, nil)
	defer shutdown()

	msg := &Message{
		Meta:         MetaConnAck,
		ConnectionID: "does-not-exist",
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
	_, ct, _, shutdown := makeAuthedClient(t, nil)
	defer shutdown()

	msg := &Message{
		ConnectionID: "does-not-exist",
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
		Callback: func(proto string, accept func() Connection) {
			if proto == inputProto {
				accept()
			}
		},
	}

	_, ct, _, shutdown := makeAuthedClient(t, ch)
	defer shutdown()

	msg := &Message{
		Meta:         MetaConnSyn,
		ConnectionID: "test-connid",
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
	clientConnChan := make(chan Connection, 1)
	ch := &callbackConnectionHandler{
		Callback: func(_ string, accept func() Connection) {
			clientConnChan <- accept()
		},
	}

	_, ct, _, shutdown := makeAuthedClient(t, ch)
	defer shutdown()

	msg := &Message{
		Meta:         MetaConnSyn,
		ConnectionID: "test-connid",
		OtherClient:  "other",
		Data:         []byte("test-proto"),
	}

	ct.incoming <- msg
	ack := <-ct.outgoing
	if ack.Meta != MetaConnAck {
		t.Error("Meta is wrong:", ack.Meta)
	}

	clientConn := <-clientConnChan
	go clientConn.Close()

	out := <-ct.outgoing
	if out.Meta != MetaConnClosed {
		t.Error("Meta is wrong:", ack.Meta)
	}
}

func TestClientAckIsAlwaysSentBeforeFirstMessage(t *testing.T) {
	respText := []byte("Bye!")
	ch := &callbackConnectionHandler{
		Callback: func(_ string, accept func() Connection) {
			conn := accept()
			conn.WriteMessage(respText)
			conn.Close()
		},
	}

	_, ct, _, shutdown := makeAuthedClient(t, ch)
	defer shutdown()

	msg := &Message{
		Meta:         MetaConnSyn,
		ConnectionID: "test-connid",
		OtherClient:  "other",
		Data:         []byte("test-proto"),
	}

	ct.incoming <- msg
	ack := <-ct.outgoing
	if ack.Meta != MetaConnAck {
		t.Error("Meta is wrong:", ack.Meta)
	}

	resp := <-ct.outgoing
	if resp.Meta != MetaNone {
		t.Error("Meta is wrong:", resp.Meta)
	} else if !bytes.Equal(respText, resp.Data) {
		t.Error("Response text is wrong:", resp.Data)
	}

	closed := <-ct.outgoing
	if closed.Meta != MetaConnClosed {
		t.Error("Meta is wrong:", closed.Meta)
	}
}

func TestClientClosesConnectionIfOtherSideClosed(t *testing.T) {
	const otherClient = "test-other-client"
	client, ct, wg, shutdown := makeAuthedClient(t, nil)
	defer shutdown()

	wg.Add(1)
	// "Main" goroutine. I'm just juggling messages and things in the
	// test after this runs
	go func() {
		defer wg.Done()
		conn, err := client.MakeConnection(otherClient, "test-proto")
		if err != nil {
			t.Fatal("Error making connection:", err)
		}

		_, err = conn.ReadMessage()
		if err != io.EOF {
			t.Error(err)
		}
	}()

	syn := <-ct.outgoing
	connID := syn.ConnectionID

	syn.Meta = MetaConnAck
	ct.incoming <- syn

	closedMsg := &Message{
		Meta:         MetaConnClosed,
		ConnectionID: connID,
		OtherClient:  otherClient,
	}

	ct.incoming <- closedMsg
}

func TestClientClosesConnectionIfOtherClientClosed(t *testing.T) {
	const (
		clientName  = "test-client"
		otherClient = "other-client"
	)

	client, ct, wg, shutdown := makeAuthedClient(t, nil)
	defer shutdown()

	wg.Add(2)
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

func TestClientAuthFailsGracefullyOnCommunicationErrors(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(3)

	// Reauth
	client0, _, _, shutdown := makeAuthedClient(t, nil)
	err := client0.Authenticate([]byte("Nope.jpg"))
	if err.Error() != errStringMultipleAuths {
		t.Error("Expected multiple auths error, got", err)
	}
	shutdown()

	// Close before send
	ct1 := newChannelTranslator()
	client1 := NewClient("client-name", &nopRWC{}, singletonTranslator(ct1), nil)

	ct1.Close()
	go func() {
		defer wg.Done()
		err := client1.Authenticate(nil)
		if err != io.EOF {
			t.Error("Expected io.EOF from Authenticate, but got", err)
		}
	}()

	// Close before recv
	ct2 := newChannelTranslator()
	client2 := NewClient("client-name", &nopRWC{}, singletonTranslator(ct2), nil)
	go func() {
		defer wg.Done()
		err := client2.Authenticate(nil)
		if err != io.EOF {
			t.Error("Expected io.EOF from Authenticate, but got", err)
		}
	}()

	<-ct2.outgoing
	ct2.Close()

	// Send back not-MetaAuthOk
	const ErrorText = "Oh noes!"
	ct3 := newChannelTranslator()
	client3 := NewClient("client-name", &nopRWC{}, singletonTranslator(ct3), nil)
	go func() {
		defer wg.Done()
		err := client3.Authenticate(nil)
		if err.Error() != ErrorText {
			t.Error("Expected", ErrorText, "from Authenticate, but got", err)
		}
	}()

	in := <-ct3.outgoing
	in.Meta = MetaAuthFailure
	in.Data = []byte(ErrorText)
	ct3.incoming <- in

	wg.Wait()
}

func TestClientCloseClosesAllConnections(t *testing.T) {
	client, ct, _, shutdown := makeAuthedClient(t, nil)
	defer shutdown()

	var conn Connection
	connMade := make(chan struct{})
	go func() {
		defer close(connMade)
		var err error
		conn, err = client.MakeConnection("test-other-client", "test-proto")
		if err != nil {
			t.Fatal("Expected connection to be made, but got", err)
		}
	}()

	syn := <-ct.outgoing
	syn.Meta = MetaConnAck
	ct.incoming <- syn

	<-connMade
	client.Close()
	_, err := conn.ReadMessage()
	if err != io.EOF {
		t.Error("Expected EOF, but got", err)
	}
}

func TestClientMakeConnectionFailsGracefullyOnWriteFailure(t *testing.T) {
	client, ct, wg, shutdown := makeAuthedClient(t, nil)
	defer shutdown()

	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := client.MakeConnection("test-other-client", "test-proto")
		if err != io.EOF {
			t.Fatal("Expected io.EOF, got", err)
		}
	}()

	ct.Close()
}

func TestClientComplainsIfRunWithoutAuthenticating(t *testing.T) {
	client := NewClient("basically-nil", nil, singletonTranslator(nil), nil)
	err := client.Run()
	if err.Error() != errStringNotYetAuthed {
		t.Error("Expected not yet authed error message, got", err)
	}
}
