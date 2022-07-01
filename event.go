package socketio

import (
	"encoding/json"
	"fmt"
	"go.uber.org/zap"
	"reflect"
	"sync"

	"github.com/vanti-dev/golang-socketio/protocol"
)

const (
	OnConnection    = "connection"
	OnDisconnection = "disconnection"
	OnError         = "error"
)

// systemEventHandler function for internal handler processing
type systemEventHandler func(c *Channel)

// event abstracts a mapping of a handler names to handler functions
type event struct {
	handlers   map[string]*handler // maps handler name to handler function representation
	handlersMu sync.RWMutex

	onConnection    systemEventHandler
	onDisconnection systemEventHandler

	logger *zap.Logger
}

// init initializes events mapping
func (e *event) init() { e.handlers = make(map[string]*handler) }

// On registers message processing function and binds it to the given event name
func (e *event) On(name string, f interface{}) error {
	c, err := newHandler(f)
	if err != nil {
		return err
	}

	e.handlersMu.Lock()
	e.handlers[name] = c
	e.handlersMu.Unlock()

	return nil
}

// findHandler returns a handler representation for the given event name
// the second parameter is true if such event found.
func (e *event) findHandler(name string) (*handler, bool) {
	e.handlersMu.RLock()
	f, ok := e.handlers[name]
	e.handlersMu.RUnlock()
	return f, ok
}

// callHandler for the given channel c and event name
func (e *event) callHandler(c *Channel, name string) {
	if e.onConnection != nil && name == OnConnection {
		e.logger.Debug("event.callHandler(): OnConnection handler")
		e.onConnection(c)
	}

	if e.onDisconnection != nil && name == OnDisconnection {
		e.onDisconnection(c)
	}

	f, ok := e.findHandler(name)
	if !ok {
		e.logger.Debug("event.callHandler(): handler not found")
		return
	}

	f.call(c, &struct{}{})
}

// processIncoming checks incoming message m on channel c
func (e *event) processIncoming(c *Channel, m *protocol.Message) {
	e.logger.Debug("event.processIncoming() fired with:", zap.Any("m", m))
	switch m.Type {
	case protocol.MessageTypeEmit:
		e.logger.Debug("event.processIncoming() is finding handler for msg.Event:", zap.String("EventName", m.EventName))
		f, ok := e.findHandler(m.EventName)
		if !ok {
			e.logger.Debug("event.processIncoming(): handler not found")
			return
		}

		e.logger.Debug("event.processIncoming() found handler:", zap.Any("f", f))

		if !f.hasArgs {
			f.call(c, &struct{}{})
			return
		}

		data := f.arguments()
		e.logger.Debug("event.processIncoming(), f.arguments() returned:", zap.Any("data", data))

		if err := json.Unmarshal([]byte(m.Args), &data); err != nil {
			e.logger.Info(fmt.Sprintf("event.processIncoming() failed to json.Unmaeshal(). msg.Args: %s, data: %v, err: %v",
				m.Args, data, err))
			return
		}

		f.call(c, data)

	case protocol.MessageTypeAckRequest:
		e.logger.Debug("event.processIncoming() ack request")
		f, ok := e.findHandler(m.EventName)
		if !ok || !f.out {
			return
		}

		var result []reflect.Value
		if f.hasArgs {
			// data type should be defined for Unmarshal()
			data := f.arguments()
			if err := json.Unmarshal([]byte(m.Args), &data); err != nil {
				return
			}
			result = f.call(c, data)
		} else {
			result = f.call(c, &struct{}{})
		}

		ackResponse := &protocol.Message{
			Type:  protocol.MessageTypeAckResponse,
			AckID: m.AckID,
		}

		c.send(ackResponse, result[0].Interface())

	case protocol.MessageTypeAckResponse:
		e.logger.Debug("event.processIncoming() ack response")
		ackC, err := c.ack.obtain(m.AckID)
		if err == nil {
			ackC <- m.Args
		}
	}
}
