// Added by YHC to verify the local inbound-request notification barrier delta.
package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
)

type connectionTestWire struct {
	messages chan []byte
}

func newConnectionTestWire() *connectionTestWire {
	return &connectionTestWire{messages: make(chan []byte, 32)}
}

func (w *connectionTestWire) Write(p []byte) (int, error) {
	w.messages <- append([]byte(nil), p...)
	return len(p), nil
}

type connectionTestPeer struct {
	input   *io.PipeWriter
	wire    *connectionTestWire
	pending map[string]map[string]any
}

func newConnectionTestPeer(t *testing.T, handler MethodHandler, hook func()) *connectionTestPeer {
	t.Helper()
	reader, writer := io.Pipe()
	peer := &connectionTestPeer{
		input:   writer,
		wire:    newConnectionTestWire(),
		pending: make(map[string]map[string]any),
	}
	conn := newConnection(handler, peer.wire, reader, hook)
	conn.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { _ = writer.Close() })
	return peer
}

func (p *connectionTestPeer) send(t *testing.T, message any) {
	t.Helper()
	b, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.input.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}

func (p *connectionTestPeer) request(t *testing.T, id, method string) {
	t.Helper()
	p.send(t, map[string]any{"jsonrpc": "2.0", "id": id, "method": method})
}

func (p *connectionTestPeer) notification(t *testing.T, method string, params any) {
	t.Helper()
	p.send(t, map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (p *connectionTestPeer) response(t *testing.T, id string) map[string]any {
	t.Helper()
	if message := p.pending[id]; message != nil {
		delete(p.pending, id)
		return message
	}
	for {
		select {
		case b := <-p.wire.messages:
			for _, line := range bytes.Split(bytes.TrimSpace(b), []byte{'\n'}) {
				var message map[string]any
				if err := json.Unmarshal(line, &message); err != nil {
					t.Fatal(err)
				}
				messageID, _ := message["id"].(string)
				if messageID == id {
					return message
				}
				if messageID != "" {
					p.pending[messageID] = message
				}
			}
		case <-t.Context().Done():
			t.Fatalf("response %q did not arrive", id)
		}
	}
}

func TestInboundRequestWaitsForPriorNotification(t *testing.T) {
	notificationStarted := make(chan struct{})
	releaseNotification := make(chan struct{})
	var releaseNotificationOnce sync.Once
	barrierReached := make(chan struct{})
	handlerStarted := make(chan struct{})
	peer := newConnectionTestPeer(t, func(_ context.Context, method string, _ json.RawMessage) (any, *RequestError) {
		switch method {
		case "notification":
			close(notificationStarted)
			<-releaseNotification
			return nil, nil
		case "request":
			close(handlerStarted)
			return map[string]bool{"ok": true}, nil
		default:
			return nil, NewMethodNotFound(method)
		}
	}, func() { close(barrierReached) })
	t.Cleanup(func() { releaseNotificationOnce.Do(func() { close(releaseNotification) }) })

	peer.notification(t, "notification", nil)
	<-notificationStarted
	peer.request(t, "request", "request")
	<-barrierReached
	select {
	case <-handlerStarted:
		t.Fatal("request handler started before the prior notification completed")
	default:
	}
	releaseNotificationOnce.Do(func() { close(releaseNotification) })
	<-handlerStarted
	if response := peer.response(t, "request"); response["error"] != nil {
		t.Fatalf("response error = %#v", response["error"])
	}
}

func TestInboundRequestBarrierCancellationSkipsHandler(t *testing.T) {
	notificationStarted := make(chan struct{})
	notificationDone := make(chan struct{})
	releaseNotification := make(chan struct{})
	var releaseNotificationOnce sync.Once
	barrierReached := make(chan struct{})
	handlerStarted := make(chan struct{})
	peer := newConnectionTestPeer(t, func(_ context.Context, method string, _ json.RawMessage) (any, *RequestError) {
		switch method {
		case "notification":
			close(notificationStarted)
			<-releaseNotification
			close(notificationDone)
			return nil, nil
		case "request":
			close(handlerStarted)
			return nil, nil
		default:
			return nil, NewMethodNotFound(method)
		}
	}, func() { close(barrierReached) })
	t.Cleanup(func() { releaseNotificationOnce.Do(func() { close(releaseNotification) }) })

	peer.notification(t, "notification", nil)
	<-notificationStarted
	peer.request(t, "request", "request")
	<-barrierReached
	peer.notification(t, "$/cancel_request", map[string]any{"requestId": "request"})
	response := peer.response(t, "request")
	errorResponse, ok := response["error"].(map[string]any)
	if !ok || errorResponse["code"] != float64(-32800) {
		t.Fatalf("cancel response = %#v", response)
	}
	releaseNotificationOnce.Do(func() { close(releaseNotification) })
	<-notificationDone
	select {
	case <-handlerStarted:
		t.Fatal("cancelled request handler started after its notification barrier released")
	default:
	}
	select {
	case message := <-peer.wire.messages:
		t.Fatalf("unexpected second response: %s", message)
	default:
	}
}

func TestInboundRequestDoesNotWaitForLaterNotification(t *testing.T) {
	barrierReached := make(chan struct{})
	handlerStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	var releaseRequestOnce sync.Once
	notificationStarted := make(chan struct{})
	releaseNotification := make(chan struct{})
	var releaseNotificationOnce sync.Once
	peer := newConnectionTestPeer(t, func(_ context.Context, method string, _ json.RawMessage) (any, *RequestError) {
		switch method {
		case "request":
			close(handlerStarted)
			<-releaseRequest
			return map[string]bool{"ok": true}, nil
		case "notification":
			close(notificationStarted)
			<-releaseNotification
			return nil, nil
		default:
			return nil, NewMethodNotFound(method)
		}
	}, func() { close(barrierReached) })
	t.Cleanup(func() {
		releaseRequestOnce.Do(func() { close(releaseRequest) })
		releaseNotificationOnce.Do(func() { close(releaseNotification) })
	})

	peer.request(t, "request", "request")
	<-barrierReached
	<-handlerStarted
	peer.notification(t, "notification", nil)
	<-notificationStarted
	releaseRequestOnce.Do(func() { close(releaseRequest) })
	if response := peer.response(t, "request"); response["error"] != nil {
		t.Fatalf("response error = %#v", response["error"])
	}
}

func TestInboundRequestsCrossingBarrierStartConcurrently(t *testing.T) {
	notificationStarted := make(chan struct{})
	releaseNotification := make(chan struct{})
	barrierReached := make(chan struct{}, 2)
	handlerStarted := make(chan string, 2)
	releaseHandlers := make(chan struct{})
	var releaseNotificationOnce sync.Once
	var releaseHandlersOnce sync.Once
	peer := newConnectionTestPeer(t, func(_ context.Context, method string, _ json.RawMessage) (any, *RequestError) {
		switch method {
		case "notification":
			close(notificationStarted)
			<-releaseNotification
			return nil, nil
		case "request-1", "request-2":
			handlerStarted <- method
			<-releaseHandlers
			return map[string]string{"method": method}, nil
		default:
			return nil, NewMethodNotFound(method)
		}
	}, func() { barrierReached <- struct{}{} })
	t.Cleanup(func() {
		releaseNotificationOnce.Do(func() { close(releaseNotification) })
		releaseHandlersOnce.Do(func() { close(releaseHandlers) })
	})

	peer.notification(t, "notification", nil)
	<-notificationStarted
	peer.request(t, "request-1", "request-1")
	peer.request(t, "request-2", "request-2")
	<-barrierReached
	<-barrierReached
	select {
	case method := <-handlerStarted:
		t.Fatalf("%s started before the prior notification completed", method)
	default:
	}
	releaseNotificationOnce.Do(func() { close(releaseNotification) })
	<-handlerStarted
	<-handlerStarted
	releaseHandlersOnce.Do(func() { close(releaseHandlers) })
	for _, id := range []string{"request-1", "request-2"} {
		if response := peer.response(t, id); response["error"] != nil {
			t.Fatalf("%s response error = %#v", id, response["error"])
		}
	}
}
