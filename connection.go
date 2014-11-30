package mp

import (
	"io"
)

// Message is the type that Clients/Servers send over the wire. Clients/Servers
// should never expose raw Message instances to the user -- this is only
// exposed so you can roll your own MessageTranslator.
type Message struct {
	Meta         MetaType
	OtherClient  string
	ConnectionID string
	Data         []byte
}

// MetaType describes the intent of a message -- some messages are meant to
// simply be delivered to a Connection, while others are meant to setup
// connections, etc.
type MetaType uint8

// Constants to represent each Meta type.
const (
	MetaNone = MetaType(iota)
	MetaWAT  // Invalid/unknown meta type received
	MetaNoSuchConnection
	MetaUnknownProto
	MetaClientClosed
	MetaConnSyn
	MetaConnAck
	MetaConnClosed
	MetaAuth
	MetaAuthOk
	MetaAuthFailure
)

// TODO: Connection has a total of one impl, so the extra flexibility that
// this interface affords may not be worth it.

// Connection is a two-way message-passing connection. When you establish a
// Connection with a client, a Connection instance is given to a client on
// each side, so both sides may communicate with each other.
//
// Guarantees we make you:
//   - Messages are guaranteed to be sent in the order you Write them. So, as
//     long as the underlying Translator implementations deliver messages in
//     order, messages will be delivered in order.
//   - One connection calling Close()
//
// Misc:
//   - If you're blocked in Write() or WriteMessage() and call Close(), then
//     Write()/WriteMessage() will not necessarily exit immediately.
//   - When Write/WriteMessage returns, we know nothing about whether or not the
//     message reached the linked Connection instance. We just know that the
//     message hit the server without issue; nothing more.
type Connection interface {
	// Note that Read([]byte) *is allowed* to concatenate messages
	// and, as a result, may drop messages with empty data. If you
	// expect to be able to receive messages with empty data, please
	// use ReadMessage.
	//
	// Similarly, Write() is allowed to merge requests into larger
	// Messages in order to increase efficiency and such.
	// Currently, it doesn't do that, but it's allowed to
	io.ReadWriteCloser

	// Reads the contents of a single message sent to us, blocking if necessary.
	// This may return a nil []byte if no Data was sent with the message.
	// If we returned a message, error is nil.
	ReadMessage() ([]byte, error)

	// Writes a message, guaranteeing that the []byte given is the full body
	// of the message. Assuming a reliable transport between Connections,
	// for every WriteMessage you do on one side, there will be a corresponding
	// ReadMessage you may do on the other.
	WriteMessage([]byte) error

	// Gets the name of the client that our other Connection resides on.
	OtherClient() string
}

// TODO: This name sucks.
// Also, a callback would probably be "easier" in this scenario, however
// I expect that any use with more than a few supported services would need to
// keep a fair amount of state with those services, so it's likely better for
// the user to just implement an interface.

// NewConnectionHandler is an interface that Clients call when a request for
// a new Connection comes in.
type NewConnectionHandler interface {
	// IncomingConnection is called when another Client wants to make a
	// connection with the calling Client. `proto` is the kind of service
	// that the initiating Client wants access to, and `accept` is the function
	// you call to get the Connection. Note that accept is finicky -- it *must*
	// be called before IncomingConnection() terminates if you want to accept the
	// Connection. Additionally, you may only call it once per call to
	// IncomingConnection. If you violate either of these, accept() panics.
	IncomingConnection(proto string, accept func() Connection)
}

// MessageTranslator is a type that can read a message from a Reader and write
// a message to a Writer in some format. Instances of MessageTranslator should
// be able to handle ReadMessage and WriteMessage being called concurrently.
type MessageTranslator interface {
	ReadMessage() (*Message, error)
	WriteMessage(*Message) error
}
