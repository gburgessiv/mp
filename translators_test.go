package mp

import (
	"bytes"
	"net"
	"reflect"
	"testing"
)

func testTranslator(t *testing.T, maker TranslatorMaker) {
	// Use a Pipe because nonblocking alternatives (i.e. bytes.Buffer)
	// may prematurely report EOF in some cases, which is undesirable.
	// ...The translator *writes* to incoming, and *reads* from outgoing.
	pipeA, pipeB := net.Pipe()
	defer pipeA.Close()
	defer pipeB.Close()

	transA := maker(pipeA, pipeA)
	transB := maker(pipeB, pipeB)

	metas := [...]MetaType{MetaNone, MetaAuthFailure}
	otherClients := [...]string{"a", "åBc 1 2 3™"}
	connIDs := [...]string{"a:1", otherClients[1] + ":ffffffff"}
	data := [...][]byte{[]byte("Hello, world!"), make([]byte, 1024)}
	for i := range data[1] {
		data[1][i] = byte(i % 256)
	}

	maxLength := len(connIDs)
	datumCopy := make([]byte, 0, 1024*1024)
	for i := 0; i < maxLength; i++ {
		datum := data[i%len(data)]
		// Defensive: In case the translator mutates the data field...
		datumCopy = append(datumCopy[:0], datum...)
		outgoingMessage := &Message{
			Meta:         metas[i%len(metas)],
			OtherClient:  otherClients[i%len(otherClients)],
			ConnectionID: connIDs[i%len(connIDs)],
			Data:         datumCopy,
		}

		outgoingCopy := *outgoingMessage
		outgoingCopy.Data = datum

		go transA.WriteMessage(outgoingMessage)
		inMessage, err := transB.ReadMessage()
		if err != nil {
			t.Error("Error receiving message on iteration", i, "err")
			continue
		}

		if !reflect.DeepEqual(inMessage, &outgoingCopy) {
			// Data can hit 1KB, and I feel as though that's not necessarily a great
			// thing to print out unless we know that said field differs.
			readDatum := inMessage.Data
			inMessage.Data = nil
			outgoingCopy.Data = nil
			if !reflect.DeepEqual(inMessage, &outgoingCopy) {
				t.Errorf("[%d] Fields in %+v and %+v differ.", i, *inMessage, outgoingCopy)
			}

			if !bytes.Equal(datum, readDatum) {
				t.Errorf("[%d] Data differs", i)
			}
		}
	}
}

var _ MessageTranslator = (*gobTranslator)(nil)
var _ MessageTranslator = (*jsonTranslator)(nil)

// I wanted different test methods for each translator, but they all
// do literally the same thing, but with a different translator.

func TestGobTranslatorEncodesAndDecodesMessagesProperly(t *testing.T) {
	testTranslator(t, NewGobTranslator)
}

func TestJSONTranslatorEncodesAndDecodesMessagesProperly(t *testing.T) {
	testTranslator(t, NewJSONTranslator)
}
