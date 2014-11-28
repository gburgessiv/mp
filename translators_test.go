package mp

import (
	"bytes"
	"math"
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
	flags := [...]MessageFlags{FlagNone, FlagNeedReadReceipt}
	seqNums := [...]uint64{0, 1, 2, math.MaxUint64 - 1, math.MaxUint64}
	otherClients := [...]string{"a", "åBc 1 2 3™"}
	connIds := [...]string{"a:1", otherClients[1] + ":ffffffff"}
	data := [...][]byte{[]byte("Hello, world!"), make([]byte, 1024)}
	for i := range data[1] {
		data[1][i] = byte(i % 256)
	}

	// If we try all possible combinations of the above, then we have to deal
	// with 2^5 * 5 = 160 combinations. I don't want to write 6 nested for loops
	// or any gnarly reflection code. So I won't.
	maxLength := len(seqNums)
	datumCopy := make([]byte, 0, 1024*1024)
	for i := 0; i < maxLength; i++ {
		datum := data[i%len(data)]
		// Defensive: In case the translator mutates the data field...
		datumCopy = append(datumCopy[:0], datum...)
		outgoingMessage := &Message{
			Meta:         metas[i%len(metas)],
			Flags:        flags[i%len(flags)],
			SeqNum:       seqNums[i%len(seqNums)],
			OtherClient:  otherClients[i%len(otherClients)],
			ConnectionId: connIds[i%len(connIds)],
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

// I wanted different test methods for each translator, but they all
// do literally the same thing, but with a different translator.

func TestGobTranslatorEncodesAndDecodesMessagesProperly(t *testing.T) {
	testTranslator(t, NewGobTranslator)
}

func TestJsonTranslatorEncodesAndDecodesMessagesProperly(t *testing.T) {
	testTranslator(t, NewJsonTranslator)
}
