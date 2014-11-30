package mp

/*
MappedConnectionHandler is a NewConnectionHandler that keeps an internal
mapping of protocol -> function. When IncomingConnection(proto, conn) is
called, this ConnectionHandler will see if the user supplied a function
for the given proto. If so, it'll fire off the function mapped to proto
in a goroutine. Otherwise, it will refuse the connection attempt.

Usage:
	h := NewMappedConnectionHandler()
	h.AddMapping("echo", func(c Connection) {
		defer c.Close()
		for {
			msg, err := c.ReadMessage()
			if err != nil {
				log.Println(err)
				return
			}
			err = c.WriteMessage(msg)
			if err != nil {
				log.Println(err)
				return
			}

		}
	})

	h.AddMapping("ping in 1 second", func(c Connection) {
		defer c.Close()
		time.Sleep(time.Second)
		c.WriteMessage([]byte("Ping!"))
	})

Please note that the func you provide gains ownership of the Connection
that is passed to it. So it's your responsibility to close the Connection
when you're done using it.
*/
type MappedConnectionHandler struct {
	chMap map[string]func(Connection)
}

// AddMapping maps a protocol name to a function to be run in a goroutine.
//
// This is not safe to be called concurrently with IncomingConnection().
func (m *MappedConnectionHandler) AddMapping(str string, fn func(Connection)) {
	m.chMap[str] = fn
}

// IncomingConnection allows us to implement the NewConnectionHandler interface
func (m *MappedConnectionHandler) IncomingConnection(proto string, accept func() Connection) {
	cb, ok := m.chMap[proto]
	if ok {
		go cb(accept())
	}
}

// NewMappedConnectionHandler Creates and initializes a new instance of
// MappedConnectionHandler
func NewMappedConnectionHandler() *MappedConnectionHandler {
	return &MappedConnectionHandler{make(map[string]func(Connection))}
}
