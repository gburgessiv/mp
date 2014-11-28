package mp

import (
	"io"
)

type MetaType uint8

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

// The raw Message type. This and the above Meta types are fully exposed in
// order to make rolling your own MessageTranslator/etc. as effortless as
// possible. The Client/Server types generally shouldn't expose raw Message
// instances directly to the user.
type Message struct {
	Meta         MetaType
	OtherClient  string
	ConnectionId string
	Data         []byte
}

// A duplexed message-passing connection. Connections can operate in two modes:
// WaitForConfirm, and not WaitForConfirm.
//
// In Not-WaitForConfirm mode, writes will return as soon as the underlying
// Write() implementation pushes the Write out.
//
// In WaitForConfirm mode, the Connection will block in Write() until we receive
// a notification that the Connection you're writing the message to has Read
// the message.
//
// Some notes for clarity:
//  > If writes in WaitForConfirm mode recieve errors, it may be that the other
//    Connection got the message, but never got the chance to respond.
//    WaitForConfirm *only* guarantees that the write was received. Not that it
//    wasn't.
//  > In either mode, messages are guaranteed to be sent in the order you
//    Write them. So, as long as the underlying Reader/Writer implementations
//    deliver messages in order, messages will be delivered in order.
//  > The mode you're in should have no bearing on how quickly you receive a
//    notification that the other side has called Close()
type Connection interface {
	// Note that Read([]byte) *is allowed* to concatenate messages
	// and, as a result, may drop messages with empty data. If you
	// expect to be able to receive messages with empty data, please
	// use ReadMessage.
	//
	// Similarly, Write() is allowed to merge requests into larger
	// Messages in order to increase efficiency and such.
	io.ReadWriteCloser

	ReadMessage() ([]byte, error)
	WriteMessage([]byte) error

	OtherClient() string
}

// TODO: This name sucks.
// Also, a callback would probably be "easier" in this scenario, however
// I expect that any use with more than a few supported services would need to
// keep a fair amount of state with those services, so it's likely better for
// the user to just implement an interface.
type NewConnectionHandler interface {
	IncomingConnection(string, Connection) bool
}

// A type that can read a message from a Reader and write a message to a Writer
// in some format. Instances of MessageTranslator are not expected to be
// used concurrently.
type MessageTranslator interface {
	ReadMessage() (*Message, error)
	WriteMessage(*Message) error
}
