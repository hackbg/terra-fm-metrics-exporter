package subscriber

import (
	"context"
	"encoding/json"
)

type Type int

const (
	WS Type = iota
)

type Event map[string]interface{}

type JsonrpcMessage struct {
	Version string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Error   *interface{}    `json:"error,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

func NewJsonrpcMessage(nonce uint64, method string, params json.RawMessage) ([]byte, error) {
	id, err := json.Marshal(nonce)
	if err != nil {
		return nil, err
	}

	msg := JsonrpcMessage{
		Version: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	return json.Marshal(msg)
}

type ISubscriber interface {
	Subscribe(ctx context.Context, method, unsubscribeMethod string, params json.RawMessage, ch chan<- json.RawMessage) error
	Request(ctx context.Context, method string, params json.RawMessage) (result json.RawMessage, err error)
	Stop()
	Type() Type
}

func NewSubscriber(endpoint string) (ISubscriber, error) {

	return NewWebsocketConnection(endpoint)
}
