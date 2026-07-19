package events

import (
	"encoding/json"
	"testing"
	"time"
)

func TestHubPublishesSanitizedEvent(t *testing.T) {
	hub := NewHub()
	stream, unsubscribe := hub.Subscribe()
	defer unsubscribe()

	hub.Publish("dashboard", map[string]string{"targetId": "target_1"})
	select {
	case event := <-stream:
		if event.Type != "dashboard" || event.ID == 0 {
			t.Fatalf("事件元数据不正确: %#v", event)
		}
		var value map[string]string
		if err := json.Unmarshal(event.Data, &value); err != nil || value["targetId"] != "target_1" {
			t.Fatalf("事件数据不正确: %s, %v", event.Data, err)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到实时事件")
	}
}
