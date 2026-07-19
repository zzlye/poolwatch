package events

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Event 是通过 SSE 发送给前台的脱敏实时事件。
type Event struct {
	ID   uint64          `json:"id"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Hub 管理所有当前在线的前台订阅者。
type Hub struct {
	mu          sync.RWMutex
	subscribers map[uint64]chan Event
	nextClient  atomic.Uint64
	nextEvent   atomic.Uint64
}

// NewHub 创建实时事件中心。
func NewHub() *Hub {
	return &Hub{subscribers: make(map[uint64]chan Event)}
}

// Publish 将一条事件广播给所有前台，慢连接只丢弃本次刷新信号。
func (h *Hub) Publish(eventType string, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	event := Event{ID: h.nextEvent.Add(1), Type: eventType, Data: data}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, subscriber := range h.subscribers {
		select {
		case subscriber <- event:
		default:
		}
	}
}

// Subscribe 注册一个带缓冲的实时事件订阅。
func (h *Hub) Subscribe() (<-chan Event, func()) {
	id := h.nextClient.Add(1)
	channel := make(chan Event, 16)
	h.mu.Lock()
	h.subscribers[id] = channel
	h.mu.Unlock()
	return channel, func() {
		h.mu.Lock()
		if current, ok := h.subscribers[id]; ok {
			delete(h.subscribers, id)
			close(current)
		}
		h.mu.Unlock()
	}
}

// ServeHTTP 保持一个只发送事件和心跳的 SSE 连接。
func (h *Hub) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	flusher, ok := response.(http.Flusher)
	if !ok {
		http.Error(response, "当前连接不支持实时更新", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Accel-Buffering", "no")
	response.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(response, ": connected\n\n")
	flusher.Flush()

	events, unsubscribe := h.Subscribe()
	defer unsubscribe()
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case event, open := <-events:
			if !open {
				return
			}
			if _, err := fmt.Fprintf(response, "id: %d\nevent: %s\ndata: %s\n\n", event.ID, event.Type, event.Data); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := fmt.Fprint(response, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
