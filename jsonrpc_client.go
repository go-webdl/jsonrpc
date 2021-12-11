package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
)

type Client struct {
	sync.Mutex
	IDStore IDStore
	Base    http.RoundTripper
}

func (client *Client) Call(ctx context.Context, url, method string, params, reply interface{}) (err error) {
	client.Lock()
	if client.IDStore == nil {
		client.IDStore = DefaultIDStore()
	}
	if client.Base == nil {
		client.Base = http.DefaultTransport
	}
	client.Unlock()

	var idSession IDSession
	if idSession, err = client.IDStore.New(); err != nil {
		return
	}

	defer checkClose(&err, idSession)

	var body []byte
	if body, err = EncodeCall(idSession.ID(), method, params); err != nil {
		return
	}

	var req *http.Request
	if req, err = http.NewRequest("POST", url, bytes.NewReader(body)); err != nil {
		return
	}

	req = req.WithContext(ctx)

	var resp *http.Response
	if resp, err = client.Base.RoundTrip(req); err != nil {
		return
	}

	defer checkClose(&err, resp.Body)
	if err = DecodeReply(resp.Body, reply); err != nil {
		return
	}
	return
}

type clientRequest struct {
	// JSON-RPC protocol.
	Version string `json:"jsonrpc"`

	// The request id. MUST be a string, number or null.
	// Our implementation will not do type checking for id.
	// It will be copied as it is.
	Id interface{} `json:"id"`

	// A String containing the name of the method to be invoked.
	Method string `json:"method"`

	// A Structured value to pass as arguments to the method.
	Params interface{} `json:"params"`
}

type clientResponse struct {
	// JSON-RPC protocol.
	Version string `json:"jsonrpc"`

	// The Object that was returned by the invoked method. This must be null
	// in case there was an error invoking the method.
	// As per spec the member will be omitted if there was an error.
	Result *json.RawMessage `json:"result,omitempty"`

	// An Error object if there was an error invoking the method. It must be
	// null if there was no error.
	// As per spec the member will be omitted if there was no error.
	Error *Error `json:"error,omitempty"`

	// This must be the same id as the request it is responding to.
	Id *json.RawMessage `json:"id"`
}

func EncodeCall(id interface{}, method string, params interface{}) (body []byte, err error) {
	return json.Marshal(&clientRequest{Version, id, method, params})
}

func DecodeReply(r io.Reader, reply interface{}) (err error) {
	var response clientResponse
	if err = json.NewDecoder(r).Decode(&response); err != nil {
		return
	}
	if response.Error != nil {
		return response.Error
	}
	if err = json.Unmarshal(*response.Result, reply); err != nil {
		return
	}
	return
}

type IDStore interface {
	New() (IDSession, error)
}

type IDSession interface {
	io.Closer
	ID() interface{}
}

type defaultIDStore struct {
	curr uint64
}

func DefaultIDStore() IDStore {
	return &defaultIDStore{}
}

func (store *defaultIDStore) New() (session IDSession, err error) {
	id := atomic.AddUint64(&store.curr, 1)
	session = &defaultIDSession{id}
	return
}

type defaultIDSession struct {
	id uint64
}

func (session *defaultIDSession) ID() interface{} {
	return session.id
}

func (session *defaultIDSession) Close() error {
	return nil
}

func checkClose(err *error, closer io.Closer) {
	if e := closer.Close(); e != nil && *err == nil {
		*err = e
	}
}
