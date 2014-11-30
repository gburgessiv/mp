/*
Package mp is a library used to pass messages between multiple processes.
MessagePasser enforces no format on the messages passed between the clients;
its only goal is to get the message from point A to point B.

Basics

The basis of MessagePasser is the Connection: a two-way channel of communication
between two processes. Connections are issued through Clients, which are all
linked to the same Server.

Clients are identified by their name -- examples of valid names range from
"desktop" to "net.gbiv.AbstractNetworkAdaptorFactoryImpl"; anything is fair
game (so long as it's not blank and doesn't contain a ':').

Connections are created in one of two ways:
	- A user calls Client.MakeConnection(proto, otherClient string)
	- A remote user requested that a new Connection be made with the
	  current client using Client.MakeConnection.

In the former case, you need to pass in the identifier for another client
that is currently connected (otherClient) and a protocol string -- the
protocol string can be any string; it's entirely user-defined and is
interpreted entirely by user code.

Assuming the protocol is recognized by `otherClient`, both the client
initiating the Connection and otherClient will then have Connections
through which they can talk with each other. The lifetime of a Connection
is bound to the lifetime of the Client that you got it from, and the lifetime
of the other Connection that it's tied to. (Naturally, you can Close() one side
of a Connection without issue)

One of the goals of this project is to be language independent; all of the
encoding/decoding of Messages is done using MessageTranslator implementations,
TODO: (Finish this after completing multiprotocol support in the Server).

Examples

See the examples/ subdirectory for examples.

*/
package mp
