package main

import (
	"fmt"
	"github.com/dyreshark/mp"
	"io"
	"log"
	"net"
	"os"
)

// Client A is your typical echo client -- if it gets a message for protocol
// "greetings", it'll wait for a message, display it, then send back "Hello World!"
func startClientA(addr string) (io.Closer, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		log.Fatal("[A] ", err)
	}

	// We have the concept of MessageHandlers -- essentially, when one client
	// wants to connect to another, it will send a "protocol" string with the
	// connection request. MappedConnectionHandlers allow you to map a protocol
	// to running an arbitrary function in a goroutine.
	mappedHandler := mp.NewMappedConnectionHandler()

	// In this example, every time we get a connection request for the "greeting"
	// protocol, we run the below func with the new connection
	mappedHandler.AddMapping("greetings", func(c mp.Connection) {
		defer c.Close()

		fmt.Println("[A] Got a new greetings connection. Someone wants to say hi!")
		msg, err := c.ReadMessage()
		if err != nil {
			fmt.Println("[A] Instead of greeting us, they gave us this:", err)
		}

		fmt.Println("[A] We got", string(msg))

		err = c.WriteMessage([]byte("Hello World!"))
		if err != nil {
			fmt.Println("[A] We couldn't write back:", err)
		} else {
			fmt.Println("[A] We said hi back!")
		}
	})

	// Setting up the client -- its name will be clientA, it will talk to the
	// server over the TCP connection we made (using Gob encoding -- see
	// "encoding/gob" for details on that), and the new connection handler will
	// be the mappedHandler we set up above.
	client := mp.NewClient("clientA", conn, mp.NewGobTranslator, mappedHandler)
	client.Authenticate([]byte("No password needed!"))
	go func() { log.Fatal(client.Run()) }()
	return conn, nil
}

// Client B spins up with the sole purpose of establishing a single Greetings
// connection with clientA. It has a short conversation with clientA, and then
// ends the connection. Ezpz.
func runClientB(addr string) error {
	netConn, err := net.Dial("tcp", addr)
	if err != nil {
		log.Fatal("[B]", err)
		return err
	}

	mappedHandler := mp.NewMappedConnectionHandler()
	// Don't want to accept any incoming connections

	client := mp.NewClient("clientB", netConn, mp.NewGobTranslator, mappedHandler)
	defer client.Close()

	client.Authenticate([]byte("No password needed!"))
	go client.Run()

	conn, err := client.MakeConnection("clientA", "greetings")
	if err != nil {
		fmt.Println("[B] Couldn't connect to clientA -- ", err)
		return err
	}

	err = conn.WriteMessage([]byte("Hello Friend!"))
	if err != nil {
		fmt.Println("[B] Error writing message", err)
		return err
	}

	msg, err := conn.ReadMessage()
	if err != nil {
		fmt.Println("[B] Error getting something back", err)
		return err
	}

	fmt.Println("[B] They sent something back:", string(msg))
	return nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: client port-number\n", os.Args[0])
		return
	}

	portStr := os.Args[1]
	addr := "127.0.0.1:" + portStr
	closer, err := startClientA(addr)
	if err != nil {
		log.Fatal("Couldn't start A:", err)
	}

	defer closer.Close()

	err = runClientB(addr)
	if err != nil {
		log.Fatal("Couldn't complete B:", err)
	}
}
