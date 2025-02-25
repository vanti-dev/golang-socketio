package transport

import (
	"errors"
	"go.uber.org/zap"
	"io/ioutil"
	"net/http"
	"strings"
	"sync"
	"time"

	"fmt"
	"github.com/vanti-dev/golang-socketio/protocol"
)

const (
	PlDefaultPingInterval   = 30 * time.Second
	PlDefaultPingTimeout    = 60 * time.Second
	PlDefaultReceiveTimeout = 60 * time.Second
	PlDefaultSendTimeout    = 60 * time.Second

	StopMessage     = "stop"
	UpgradedMessage = "upgrade"
	noError         = "0"

	hijackingNotSupported = "webserver doesn't support hijacking"
)

var (
	errGetMessageTimeout       = errors.New("timeout waiting for the message")
	errReceivedConnectionClose = errors.New("received connection close")
	errWriteMessageTimeout     = errors.New("timeout waiting for write")
)

// withLength returns s as a message with length
func withLength(m string) string { return fmt.Sprintf("%d:%s", len(m), m) }

// setHeaders into w
func setHeaders(w http.ResponseWriter) {
	// We are going to return JSON no matter what:
	w.Header().Set("Content-Type", "application/json")
	// Don't cache response:
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate") // HTTP 1.1
	w.Header().Set("Pragma", "no-cache")                                   // HTTP 1.0
	w.Header().Set("Expires", "0")                                         // Proxies
}

// PollingTransportParams represents XHR polling transport params
type PollingTransportParams struct {
	Headers http.Header
}

// sessions describes sessions needed for identifying polling connections with socket.io connections
type sessions struct {
	sync.Mutex
	m      map[string]*PollingConnection
	logger *zap.Logger
}

// Set sets sessionID to the given connection
func (s *sessions) Set(sessionID string, conn *PollingConnection) {
	s.logger.Debug("sessions.Set() fired with:", zap.String("sessionId", sessionID))
	s.Lock()
	defer s.Unlock()
	s.m[sessionID] = conn
}

// Delete the sessionID
func (s *sessions) Delete(sessionID string) {
	s.logger.Debug("sessions.Delete() fired with:", zap.String("sessionId", sessionID))
	s.Lock()
	defer s.Unlock()
	delete(s.m, sessionID)
}

// Get returns polling connection if it exists, otherwise returns nil
func (s *sessions) Get(sessionID string) *PollingConnection {
	s.Lock()
	defer s.Unlock()
	return s.m[sessionID]
}

// PollingTransport represens the XHR polling transport params
type PollingTransport struct {
	PingInterval   time.Duration
	PingTimeout    time.Duration
	ReceiveTimeout time.Duration
	SendTimeout    time.Duration

	Headers  http.Header
	sessions sessions

	logger *zap.Logger
}

// DefaultPollingTransport returns PollingTransport with default params
func DefaultPollingTransport() *PollingTransport {
	l, _ := zap.NewProduction()
	return &PollingTransport{
		PingInterval:   PlDefaultPingInterval,
		PingTimeout:    PlDefaultPingTimeout,
		ReceiveTimeout: PlDefaultReceiveTimeout,
		SendTimeout:    PlDefaultSendTimeout,
		sessions: sessions{
			Mutex:  sync.Mutex{},
			m:      map[string]*PollingConnection{},
			logger: l,
		},
		Headers: nil,
		logger:  l,
	}
}

func NewPollingTransport(logger *zap.Logger) *PollingTransport {
	t := DefaultPollingTransport()
	t.logger = logger
	return t
}

// Connect for the polling transport is a placeholder
func (t *PollingTransport) Connect(url string) (Connection, error) {
	return nil, nil
}

// HandleConnection returns a pointer to a new Connection
func (t *PollingTransport) HandleConnection(w http.ResponseWriter, r *http.Request) (Connection, error) {
	return &PollingConnection{
		Transport:  t,
		eventsInC:  make(chan string),
		eventsOutC: make(chan string),
		errors:     make(chan string),
	}, nil
}

// SetSid to the given sessionID and connection
func (t *PollingTransport) SetSid(sessionID string, connection Connection) {
	t.sessions.Set(sessionID, connection.(*PollingConnection))
	connection.(*PollingConnection).sessionID = sessionID
}

// Serve is for receiving messages from client, simple decoding also here
func (t *PollingTransport) Serve(w http.ResponseWriter, r *http.Request) {
	sessionId := r.URL.Query().Get("sid")
	conn := t.sessions.Get(sessionId)
	if conn == nil {
		return
	}

	switch r.Method {
	case http.MethodGet:
		t.logger.Debug("PollingTransport.Serve() is serving GET request")
		conn.PollingWriter(w, r)
	case http.MethodPost:
		bodyBytes, err := ioutil.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			t.logger.Warn("PollingTransport.Serve() error ioutil.ReadAll():", zap.Error(err))
			return
		}

		bodyString := string(bodyBytes)
		t.logger.Debug("PollingTransport.Serve() POST bodyString before split:", zap.String("bodyString", bodyString))
		index := strings.Index(bodyString, ":")
		body := bodyString[index+1:]

		setHeaders(w)

		t.logger.Debug("PollingTransport.Serve() POST body:", zap.String("body", body))
		w.Write([]byte("ok"))
		t.logger.Debug("PollingTransport.Serve() written POST response")
		conn.eventsInC <- body
		t.logger.Debug("PollingTransport.Serve() sent to eventsInC")
	}
}

// PollingConnection represents a XHR polling connection
type PollingConnection struct {
	Transport  *PollingTransport
	eventsInC  chan string
	eventsOutC chan string
	errors     chan string
	sessionID  string
}

// GetMessage waits for incoming message from the connection
func (polling *PollingConnection) GetMessage() (string, error) {
	select {
	case <-time.After(polling.Transport.ReceiveTimeout):
		polling.Transport.logger.Debug("PollingConnection.GetMessage() timed out")
		return "", errGetMessageTimeout
	case m := <-polling.eventsInC:
		polling.Transport.logger.Debug("PollingConnection.GetMessage() received:", zap.String("m", m))
		if m == protocol.MessageClose {
			polling.Transport.logger.Debug("PollingConnection.GetMessage() received connection close")
			return "", errReceivedConnectionClose
		}
		return m, nil
	}
}

// WriteMessage to the connection
func (polling *PollingConnection) WriteMessage(message string) error {
	polling.Transport.logger.Debug("PollingConnection.WriteMessage() fired with:", zap.String("message", message))
	polling.eventsOutC <- message
	polling.Transport.logger.Debug("PollingConnection.WriteMessage() written to eventsOutC:", zap.String("message", message))
	select {
	case <-time.After(polling.Transport.SendTimeout):
		return errWriteMessageTimeout
	case errString := <-polling.errors:
		if errString != noError {
			polling.Transport.logger.Debug("PollingConnection.WriteMessage() failed to write with err:", zap.String("errString", errString))
			return errors.New(errString)
		}
	}
	return nil
}

// Close the polling connection and delete session
func (polling *PollingConnection) Close() error {
	polling.Transport.logger.Debug("PollingConnection.Close() fired for session:", zap.String("sessionId", polling.sessionID))
	err := polling.WriteMessage(protocol.MessageBlank)
	polling.Transport.sessions.Delete(polling.sessionID)
	return err
}

// PingParams returns a connection ping params
func (polling *PollingConnection) PingParams() (time.Duration, time.Duration) {
	return polling.Transport.PingInterval, polling.Transport.PingTimeout
}

// PollingWriter for writing polling answer
func (polling *PollingConnection) PollingWriter(w http.ResponseWriter, r *http.Request) {
	setHeaders(w)
	select {
	case <-time.After(polling.Transport.SendTimeout):
		polling.Transport.logger.Debug("PollingTransport.PollingWriter() timed out")
		polling.errors <- noError
	case message := <-polling.eventsOutC:
		polling.Transport.logger.Debug("PollingTransport.PollingWriter() prepares to write message:", zap.String("message", message))
		message = withLength(message)
		if message == withLength(protocol.MessageBlank) {
			polling.Transport.logger.Debug("PollingTransport.PollingWriter() writing 1:6")

			hj, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, hijackingNotSupported, http.StatusInternalServerError)
				return
			}

			conn, buffer, err := hj.Hijack()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			defer conn.Close()

			buffer.WriteString("HTTP/1.1 200 OK\r\n" +
				"Cache-Control: no-cache, private\r\n" +
				"Content-Length: 3\r\n" +
				"Date: Mon, 24 Nov 2016 10:21:21 GMT\r\n\r\n")
			buffer.WriteString(withLength(protocol.MessageBlank))
			buffer.Flush()
			polling.Transport.logger.Debug("PollingTransport.PollingWriter() hijack returns")
			polling.errors <- noError
			polling.eventsInC <- StopMessage
		} else {
			_, err := w.Write([]byte(message))
			polling.Transport.logger.Debug("PollingTransport.PollingWriter() written message:", zap.String("message", message))
			if err != nil {
				polling.Transport.logger.Warn("PollingTransport.PollingWriter() failed to write message with err:", zap.Error(err))
				polling.errors <- err.Error()
				return
			}
			polling.errors <- noError
		}
	}
}
