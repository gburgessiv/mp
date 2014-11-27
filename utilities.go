package mp

type MappedConnectionHandler struct {
  chMap map[string]func(Connection)
}

func (m *MappedConnectionHandler) AddMapping(str string, fn func(Connection)) {
  m.chMap[str] = fn
}

func (m *MappedConnectionHandler) IncomingConnection(proto string, conn Connection) bool {
  cb, ok := m.chMap[proto]
  if !ok {
    return false
  }

  go cb(conn)
  return true
}

func NewMappedConnectionHandler() *MappedConnectionHandler {
  return &MappedConnectionHandler{make(map[string]func(Connection))}
}
