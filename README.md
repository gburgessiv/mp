MessagePasser
=============

MessagePasser is an implementation of a cross-machine messaging protocol. 
Currently, it's in its super-early stages of development, so not much 
exists (sans a Client+Server implementation in Go). At the time of writing,
it's version 0.01a. I'm using it a bit "in production", but it's still
very much in development, and the user-facing API isn't set in stone yet.

Overview
--------

The goal of MessagePasser (MP) is to allow two processes to talk to each other, 
plain and simple. MP takes a client-server model, so it doesn't matter if the
two processes are both on localhost, nor does it matter if they're both behind
NATs. So long as the server is accessible, they can talk.

The base unit of communication in MP is the _Connection_. This is a bidirectional
channel between two parties, which either party may use to send bytes to the
other. Connections are managed by _Clients_, who all talk to the _Server_, which
is just a glorified router (with user authentication built in).

Authentication to the server is super simple -- it looks something like this:

1. Client sends name + secret (or username and password, if you'd prefer),
   to the Server
2. If the Server likes this name + secret, it acknowledges that everything's
   good. Otherwise, it closes the connection.
3. Client can now talk to other clients.

In order to start talking to another Client (establish a Connection), you need
to have the other Client's name and the type of Connection you want to establish
(protocol name). Both of these are strings. If you can provide both of those things,
you have all you need to set up a Connection.

Quick Start
-----------

*Server*

It's highly recommended that you just use the default server in 
examples/server.go. Simply run it with `go run server.go [port-number]`.
If you do use it, *PLEASE* change the authentication function. *ANYONE*
is allowed in using the default one. This is probably not what you want.

Important parts of the server:
- Auth functions take a string and a secret, and they return whether or not
  that pair of credentials is valid. 
- Translators are interfaces we use to put a \*Message on to the wire. So yes,
  it's entirely possible to have your Server speak JSON with a few extra lines
  of code.

*Client*

A simple setup with two clients (i.e. a friendly echo server) is located in
examples/client.go. It details how to make new clients, connections, etc.

Points of note for the clients:
- See note on Translators above for the Server section
- NewConnectionHandlers are how we determine how to handle an incoming connection
  request, based on the protocol that the new connection has specified.

Points of note for connections:
- When one side closes, the other side is not (yet) notified to close. You need
  to write to get an "unreachable" notification -- this will be fixed very soon.
- Read/Write are synonymous (sans signatures) to ReadMessage/WriteMessage.

Hopefully we'll have docs at some point. :)

Immediate TODOs
---------------

- Finish up docs
- Adding support for multiple message formats (JSON, Gob, Protobuf, etc.) 
  talking to the same server at once.
- Testing testing testing testing testing

Future Plans
------------

- Client implementations in other languages (Java, Python, JS, ...)

I found a bug!
--------------

You mean you found my latest feature! Bug reports/pull requests are welcome :)
