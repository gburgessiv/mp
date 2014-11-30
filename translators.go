package mp

import (
	"encoding/gob"
	"encoding/json"
	"io"
)

// Translates messages using the built-in encoding/gob module
type gobTranslator struct {
	dec *gob.Decoder
	enc *gob.Encoder
}

// NewGobTranslator creates a new MessageTranslator that reads/write messages
// using the Gob message format.
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

type jsonTranslator struct {
	dec *json.Decoder
	enc *json.Encoder
}

// NewJSONTranslator creates a new MessageTranslator that reads/write messages
// using the JSON message format.
func NewJSONTranslator(r io.Reader, w io.Writer) MessageTranslator {
	return &jsonTranslator{json.NewDecoder(r), json.NewEncoder(w)}
}

func (t *jsonTranslator) ReadMessage() (*Message, error) {
	msg := &Message{}
	err := t.dec.Decode(msg)
	return msg, err
}

func (t *jsonTranslator) WriteMessage(m *Message) error {
	return t.enc.Encode(m)
}
