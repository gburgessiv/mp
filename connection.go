package mp

import (
  "io"
)

type MetaType uint8

const (
  MetaNone = MetaType(iota)
  MetaWAT // Invalid/unknown meta type received
  MetaNoSuchConnection
  MetaConnSyn
  MetaConnAck
  MetaAuth
  MetaAuthOk
  MetaAuthFailure
)

type Message struct {
  Meta MetaType
  SeqNum uint64
  OtherClient string
  ConnectionId string
  Data []byte
}

type Connection interface {
  io.ReadWriteCloser

  ReadMessage() ([]byte, error)
  WriteMessage([]byte) error

  OtherClient() string

  SetWaitForConfirm(bool)
  WaitsForConfirm() bool
}

type MessageTranslator interface {
  ReadFrom(io.Reader) (*Message, error)
  WriteTo(io.Writer, *Message) error
}
