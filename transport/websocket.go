package transport

import (
	"crypto/tls"
	"errors"
	"go.uber.org/zap"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const (
	upgradeFailed = "Upgrade failed: "

	wsDefaultPingInterval   = 30 * time.Second
	wsDefaultPingTimeout    = 60 * time.Second
	wsDefaultReceiveTimeout = 60 * time.Second
	wsDefaultSendTimeout    = 60 * time.Second
	wsDefaultBufferSize     = 1024 * 32
)

// WebsocketTransportParams is a parameters for getting non-default websocket transport
type WebsocketTransportParams struct {
	Headers         http.Header
	TLSClientConfig *tls.Config
}

var (
	errBinaryMessage     = errors.New("binary messages are not supported")
	errBadBuffer         = errors.New("buffer error")
	errPacketWrong       = errors.New("wrong packet type error")
	errMethodNotAllowed  = errors.New("method not allowed")
	errHttpUpgradeFailed = errors.New("http upgrade failed")
)

// WebsocketTransport implements websocket transport
type WebsocketTransport struct {
	PingInterval   time.Duration
	PingTimeout    time.Duration
	ReceiveTimeout time.Duration
	SendTimeout    time.Duration

	BufferSize      int
	Headers         http.Header
	TLSClientConfig *tls.Config

	CheckOriginHandler func(r *http.Request) bool
	logger             *zap.Logger
}

// DefaultWebsocketTransport returns websocket connection with default params
func DefaultWebsocketTransport() *WebsocketTransport {
	l, _ := zap.NewProduction()
	return &WebsocketTransport{
		PingInterval:   wsDefaultPingInterval,
		PingTimeout:    wsDefaultPingTimeout,
		ReceiveTimeout: wsDefaultReceiveTimeout,
		SendTimeout:    wsDefaultSendTimeout,
		BufferSize:     wsDefaultBufferSize,
		logger:         l,
	}
}

// NewWebsocketTransport returns websocket transport with given params
func NewWebsocketTransport(params WebsocketTransportParams, originHandler func(r *http.Request) bool, logger *zap.Logger) *WebsocketTransport {
	tr := DefaultWebsocketTransport()
	tr.Headers = params.Headers
	tr.TLSClientConfig = params.TLSClientConfig
	tr.CheckOriginHandler = originHandler
	tr.logger = logger
	return tr
}

// Connect to the given url
func (t *WebsocketTransport) Connect(url string) (Connection, error) {
	dialer := websocket.Dialer{TLSClientConfig: t.TLSClientConfig}
	socket, _, err := dialer.Dial(url, t.Headers)
	if err != nil {
		return nil, err
	}
	return &WebsocketConnection{socket, t}, nil
}

// HandleConnection
func (t *WebsocketTransport) HandleConnection(w http.ResponseWriter, r *http.Request) (Connection, error) {
	t.logger.Debug("HandleConnection", zap.Any("r.Method", r.Method))
	if r.Method != http.MethodGet {
		http.Error(w, upgradeFailed+errMethodNotAllowed.Error(), http.StatusServiceUnavailable)
		return nil, errMethodNotAllowed
	}

	u := &websocket.Upgrader{
		ReadBufferSize:  t.BufferSize,
		WriteBufferSize: t.BufferSize,
	}
	if t.CheckOriginHandler != nil {
		u.CheckOrigin = t.CheckOriginHandler
	}

	socket, err := u.Upgrade(w, r, nil)
	if err != nil {
		t.logger.Warn("couldn't upgrade", zap.Error(err))
		http.Error(w, upgradeFailed+err.Error(), http.StatusServiceUnavailable)
		return nil, errHttpUpgradeFailed
	}

	return &WebsocketConnection{socket, t}, nil
}

// Serve does nothing here. Websocket connection does not require any additional processing
func (t *WebsocketTransport) Serve(w http.ResponseWriter, r *http.Request) {}

// SetSid does nothing for the websocket transport, it's used only when transport changes (from)
func (t *WebsocketTransport) SetSid(string, Connection) {}

// WebsocketConnection represents websocket connection
type WebsocketConnection struct {
	socket    *websocket.Conn
	transport *WebsocketTransport
}

// GetMessage from the connection
func (ws *WebsocketConnection) GetMessage() (string, error) {
	ws.transport.logger.Debug("WebsocketConnection.GetMessage() fired")
	ws.socket.SetReadDeadline(time.Now().Add(ws.transport.ReceiveTimeout))

	msgType, reader, err := ws.socket.NextReader()
	if err != nil {
		ws.transport.logger.Debug("WebsocketConnection.GetMessage() ws.socket.NextReader() err:", zap.Error(err))
		return "", err
	}

	// supports only text messages exchange
	if msgType != websocket.TextMessage {
		ws.transport.logger.Debug("WebsocketConnection.GetMessage() returns errBinaryMessage")
		return "", errBinaryMessage
	}

	data, err := ioutil.ReadAll(reader)
	if err != nil {
		ws.transport.logger.Debug("WebsocketConnection.GetMessage() returns errBadBuffer")
		return "", errBadBuffer
	}

	text := string(data)
	ws.transport.logger.Debug("WebsocketConnection.GetMessage() text:", zap.String("text", text))

	// empty messages are not allowed
	if len(text) == 0 {
		ws.transport.logger.Debug("WebsocketConnection.GetMessage() returns errPacketWrong")
		return "", errPacketWrong
	}

	return text, nil
}

// WriteMessage message m into a connection
func (ws *WebsocketConnection) WriteMessage(m string) error {
	ws.transport.logger.Debug("WebsocketConnection.WriteMessage() fired with:", zap.String("m", m))
	ws.socket.SetWriteDeadline(time.Now().Add(ws.transport.SendTimeout))

	writer, err := ws.socket.NextWriter(websocket.TextMessage)
	if err != nil {
		return err
	}

	if _, err := writer.Write([]byte(m)); err != nil {
		return err
	}

	return writer.Close()
}

// Close the connection
func (ws *WebsocketConnection) Close() error {
	ws.transport.logger.Debug("WebsocketConnection.Close() fired")
	return ws.socket.Close()
}

// PingParams returns ping params
func (ws *WebsocketConnection) PingParams() (time.Duration, time.Duration) {
	return ws.transport.PingInterval, ws.transport.PingTimeout
}
