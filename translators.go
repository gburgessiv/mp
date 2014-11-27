package mp

import (
	"encoding/gob"
	"io"
)

// Translates messages using the built-in encoding/gob module
type gobTranslator struct {
	dec *gob.Decoder
	enc *gob.Encoder
}

func NewGobTranslator(r io.Reader, w io.Writer) MessageTranslator {
	return &gobTranslator{gob.NewDecoder(r), gob.NewEncoder(w)}
}

func (t *gobTranslator) ReadMessage() (*Message, error) {
	msg := &Message{}
	err := t.dec.Decode(msg)
	return msg, err
}

func (t *gobTranslator) WriteMessage(m *Message) error {
	return t.enc.Encode(m)
}
