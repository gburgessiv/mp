package main

import (
	"fmt"
	"github.com/dyreshark/mp"
	"log"
	"net"
	"os"
)

// An authentication function that will allow any client to connect.
// Probably not the best idea in production...
func authAny(name string, secret []byte) bool {
	return true
}

func main() {
	if len(os.Args) < 2 {
		fmt.Printf("Usage: server port-number", os.Args[0])
		return
	}

	portStr := os.Args[1]
	addr, err := net.ResolveTCPAddr("tcp", "0.0.0.0:"+portStr)
	if err != nil {
		log.Fatal(err)
	}

	listener, err := net.ListenTCP("tcp", addr)
	// Create a new server that will allow any client name/secret combo to connect
	// to it (probably not the best idea), and that speaks using gob encoding
	// (see docs for "encoding/gob" for more info)
	server := mp.NewServer(authAny, mp.NewGobTranslator)
	log.Fatal(server.Listen(listener))
}
