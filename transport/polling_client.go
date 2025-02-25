package transport

import (
	"bytes"
	"encoding/json"
	"errors"
	"go.uber.org/zap"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	"github.com/vanti-dev/golang-socketio/protocol"
)

var (
	errResponseIsNotOK       = errors.New("response body is not OK")
	errAnswerNotOpenSequence = errors.New("not opensequence answer")
	errAnswerNotOpenMessage  = errors.New("not openmessage answer")
)

// openSequence represents a connection open sequence parameters
type openSequence struct {
	Sid          string        `json:"sid"`
	Upgrades     []string      `json:"upgrades"`
	PingInterval time.Duration `json:"pingInterval"`
	PingTimeout  time.Duration `json:"pingTimeout"`
}

// PollingClientTransport represents polling client transport parameters
type PollingClientTransport struct {
	PingInterval   time.Duration
	PingTimeout    time.Duration
	ReceiveTimeout time.Duration
	SendTimeout    time.Duration

	Headers  http.Header
	sessions sessions

	logger *zap.Logger
}

// DefaultPollingClientTransport returns client polling transport with default params
func DefaultPollingClientTransport() *PollingClientTransport {
	return &PollingClientTransport{
		PingInterval:   PlDefaultPingInterval,
		PingTimeout:    PlDefaultPingTimeout,
		ReceiveTimeout: PlDefaultReceiveTimeout,
		SendTimeout:    PlDefaultSendTimeout,
	}
}

func NewPollingClientTransport(logger *zap.Logger) *PollingClientTransport {
	t := DefaultPollingClientTransport()
	t.logger = logger
	return t
}

// HandleConnection for the polling client is a placeholder
func (t *PollingClientTransport) HandleConnection(w http.ResponseWriter, r *http.Request) (Connection, error) {
	return nil, nil
}

// Serve for the polling client is a placeholder
func (t *PollingClientTransport) Serve(w http.ResponseWriter, r *http.Request) {}

// SetSid for the polling client is a placeholder
func (t *PollingClientTransport) SetSid(sid string, conn Connection) {}

// Connect to server, perform 3 HTTP requests in connecting sequence
func (t *PollingClientTransport) Connect(url string) (Connection, error) {
	polling := &PollingClientConnection{transport: t, client: &http.Client{}, url: url}

	resp, err := polling.client.Get(polling.url)
	if err != nil {
		t.logger.Debug("PollingConnection.Connect() error polling.client.Get() 1:", zap.Error(err))
		return nil, err
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.logger.Debug("PollingConnection.Connect() error ioutil.ReadAll() 1:", zap.Error(err))
		return nil, err
	}

	resp.Body.Close()
	bodyString := string(bodyBytes)
	t.logger.Debug("PollingConnection.Connect() bodyString 1:", zap.String("bodyString", bodyString))

	body := bodyString[strings.Index(bodyString, ":")+1:]
	if string(body[0]) != protocol.MessageOpen {
		return nil, errAnswerNotOpenSequence
	}

	bodyBytes2 := []byte(body[1:])
	var openSequence openSequence

	if err := json.Unmarshal(bodyBytes2, &openSequence); err != nil {
		t.logger.Debug("PollingConnection.Connect() error json.Unmarshal() 1:", zap.Error(err))
		return nil, err
	}

	polling.url += "&sid=" + openSequence.Sid
	t.logger.Debug("PollingConnection.Connect() polling.url 1:", zap.String("url", polling.url))

	resp, err = polling.client.Get(polling.url)
	if err != nil {
		t.logger.Debug("PollingConnection.Connect() error plc.client.Get() 2:", zap.Error(err))
		return nil, err
	}

	bodyBytes, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.logger.Debug("PollingConnection.Connect() error ioutil.ReadAll() 2:", zap.Error(err))
		return nil, err
	}

	resp.Body.Close()
	bodyString = string(bodyBytes)
	t.logger.Debug("PollingConnection.Connect() bodyString 2:", zap.String("bodyString", bodyString))
	body = bodyString[strings.Index(bodyString, ":")+1:]

	if body != protocol.MessageEmpty {
		return nil, errAnswerNotOpenMessage
	}

	return polling, nil
}

// PollingClientConnection represents XHR polling client connection
type PollingClientConnection struct {
	transport *PollingClientTransport
	client    *http.Client
	url       string
	sid       string
}

// GetMessage performs a GET request to wait for the following message
func (polling *PollingClientConnection) GetMessage() (string, error) {
	polling.transport.logger.Debug("PollingConnection.GetMessage() fired")

	resp, err := polling.client.Get(polling.url)
	if err != nil {
		polling.transport.logger.Warn("PollingConnection.GetMessage() error polling.client.Get():", zap.Error(err))
		return "", err
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		polling.transport.logger.Warn("PollingConnection.GetMessage() error ioutil.ReadAll():", zap.Error(err))
		return "", err
	}

	bodyString := string(bodyBytes)
	polling.transport.logger.Debug("PollingConnection.GetMessage() ", zap.String("bodyString", bodyString))
	index := strings.Index(bodyString, ":")

	body := bodyString[index+1:]
	return body, nil
}

// WriteMessage performs a POST request to send a message to server
func (polling *PollingClientConnection) WriteMessage(m string) error {
	mWrite := withLength(m)
	polling.transport.logger.Debug("PollingConnection.WriteMessage() fired, msgToWrite:", zap.String("mWrite", mWrite))
	mJSON := []byte(mWrite)

	resp, err := polling.client.Post(polling.url, "application/json", bytes.NewBuffer(mJSON))
	if err != nil {
		polling.transport.logger.Debug("PollingConnection.WriteMessage() error polling.client.Post():", zap.Error(err))
		return err
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		polling.transport.logger.Debug("PollingConnection.WriteMessage() error ioutil.ReadAll():", zap.Error(err))
		return err
	}

	resp.Body.Close()
	bodyString := string(bodyBytes)
	if bodyString != "ok" {
		return errResponseIsNotOK
	}

	return nil
}

// Close the client connection gracefully
func (polling *PollingClientConnection) Close() error {
	return polling.WriteMessage(protocol.MessageClose)
}

// PingParams returns PingInterval and PingTimeout params
func (polling *PollingClientConnection) PingParams() (time.Duration, time.Duration) {
	return polling.transport.PingInterval, polling.transport.PingTimeout
}
