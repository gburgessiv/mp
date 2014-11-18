package main

import (
  "github.com/dyreshark/mp"
  "os"
  "log"
  "fmt"
  "net"
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

  // Validating that the 
  portStr := os.Args[1]
  addr, err := net.ResolveTCPAddr("tcp", "0.0.0.0:" + portStr)
  if err != nil {
    log.Fatal(err)
  }

  listener, err := net.ListenTCP("tcp", addr)
  server := mp.NewServer(authAny, mp.NewGobTranslator)
  server.Listen(listener)
}
