package bridge

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestNewStateStore(t *testing.T) {
	store := NewStateStore()
	if store == nil {
		t.Fatal("NewStateStore returned nil")
		return
	}
	if store.Version() != 0 {
		t.Errorf("initial version = %d, want 0", store.Version())
	}
	if store.ObserverCount() != 0 {
		t.Errorf("initial observer count = %d, want 0", store.ObserverCount())
	}
	if store.ClientCount() != 0 {
		t.Errorf("initial client count = %d, want 0", store.ClientCount())
	}
}

func TestStateStore_SetAndGet(t *testing.T) {
	store := NewStateStore()

	store.Set(FieldCurrentModel, "claude-3")
	if got := store.GetString(FieldCurrentModel); got != "claude-3" {
		t.Errorf("GetString(FieldCurrentModel) = %q, want %q", got, "claude-3")
	}

	store.Set(FieldTurnCount, 5)
	if got := store.GetInt(FieldTurnCount); got != 5 {
		t.Errorf("GetInt(FieldTurnCount) = %d, want 5", got)
	}

	store.Set(FieldIsCompacting, true)
	if got := store.GetBool(FieldIsCompacting); !got {
		t.Error("GetBool(FieldIsCompacting) = false, want true")
	}

	// Version should increment with each Set
	if v := store.Version(); v != 3 {
		t.Errorf("version after 3 Sets = %d, want 3", v)
	}
}

func TestStateStore_SetReturnsChange(t *testing.T) {
	store := NewStateStore()
	store.Set(FieldCurrentModel, "old-model")

	change := store.Set(FieldCurrentModel, "new-model")

	if change.Field != FieldCurrentModel {
		t.Errorf("change.Field = %q, want %q", change.Field, FieldCurrentModel)
	}
	if change.Topic != TopicModel {
		t.Errorf("change.Topic = %q, want %q", change.Topic, TopicModel)
	}
	if change.OldValue != "old-model" {
		t.Errorf("change.OldValue = %v, want %q", change.OldValue, "old-model")
	}
	if change.NewValue != "new-model" {
		t.Errorf("change.NewValue = %v, want %q", change.NewValue, "new-model")
	}
	if change.Timestamp.IsZero() {
		t.Error("change.Timestamp is zero")
	}
	if change.Version != 2 {
		t.Errorf("change.Version = %d, want 2", change.Version)
	}
}

func TestStateStore_Update_Batch(t *testing.T) {
	store := NewStateStore()

	changes := store.Update(map[StateField]any{
		FieldCurrentModel:       "claude-3",
		FieldConversationStatus: StatusThinking,
		FieldInputTokens:        100,
	})

	if len(changes) != 3 {
		t.Fatalf("Update returned %d changes, want 3", len(changes))
	}

	// All changes should have the same timestamp (batch atomicity).
	ts := changes[0].Timestamp
	for i, c := range changes {
		if !c.Timestamp.Equal(ts) {
			t.Errorf("change[%d].Timestamp differs from change[0].Timestamp", i)
		}
	}

	// Verify state was applied.
	if got := store.GetString(FieldCurrentModel); got != "claude-3" {
		t.Errorf("after Update, FieldCurrentModel = %q, want %q", got, "claude-3")
	}
	if got := store.Get(FieldConversationStatus); got != StatusThinking {
		t.Errorf("after Update, FieldConversationStatus = %v, want %v", got, StatusThinking)
	}
	if got := store.GetInt(FieldInputTokens); got != 100 {
		t.Errorf("after Update, FieldInputTokens = %d, want 100", got)
	}
}

func TestStateStore_Update_Empty(t *testing.T) {
	store := NewStateStore()
	changes := store.Update(nil)
	if changes != nil {
		t.Errorf("Update(nil) returned %v, want nil", changes)
	}
	changes = store.Update(map[StateField]any{})
	if changes != nil {
		t.Errorf("Update(empty) returned %v, want nil", changes)
	}
}

func TestStateStore_Snapshot(t *testing.T) {
	store := NewStateStore()
	store.Set(FieldCurrentModel, "claude-3")
	store.Set(FieldTurnCount, 10)
	store.Set(FieldSessionID, "sess-123")

	snap := store.Snapshot()

	if snap.GetString(FieldCurrentModel) != "claude-3" {
		t.Errorf("snapshot FieldCurrentModel = %q, want %q", snap.GetString(FieldCurrentModel), "claude-3")
	}
	if snap.GetInt(FieldTurnCount) != 10 {
		t.Errorf("snapshot FieldTurnCount = %d, want 10", snap.GetInt(FieldTurnCount))
	}
	if snap.GetString(FieldSessionID) != "sess-123" {
		t.Errorf("snapshot FieldSessionID = %q, want %q", snap.GetString(FieldSessionID), "sess-123")
	}
	if snap.Version != store.Version() {
		t.Errorf("snapshot version = %d, want %d", snap.Version, store.Version())
	}

	// Verify snapshot is immutable: changing store doesn't affect snapshot.
	store.Set(FieldCurrentModel, "claude-4")
	if snap.GetString(FieldCurrentModel) != "claude-3" {
		t.Error("snapshot was mutated by subsequent store.Set")
	}

	// Verify modifying snapshot map doesn't affect store.
	snap.Fields[FieldCurrentModel] = "hacked"
	if store.GetString(FieldCurrentModel) != "claude-4" {
		t.Error("store was mutated by modifying snapshot")
	}
}

func TestStateStore_SnapshotSlice(t *testing.T) {
	store := NewStateStore()
	store.Set(FieldCurrentModel, "claude-3")
	store.Set(FieldTurnCount, 10)
	store.Set(FieldInputTokens, 500)

	// Slice for TopicModel only.
	snap := store.SnapshotSlice(TopicModel)
	if snap.GetString(FieldCurrentModel) != "claude-3" {
		t.Errorf("slice should contain FieldCurrentModel")
	}
	if snap.GetInt(FieldTurnCount) != 0 {
		t.Errorf("slice should NOT contain FieldTurnCount, got %d", snap.GetInt(FieldTurnCount))
	}
	if snap.GetInt(FieldInputTokens) != 0 {
		t.Errorf("slice should NOT contain FieldInputTokens, got %d", snap.GetInt(FieldInputTokens))
	}

	// Slice for TopicAll returns everything.
	snapAll := store.SnapshotSlice(TopicAll)
	if snapAll.GetString(FieldCurrentModel) != "claude-3" {
		t.Errorf("TopicAll slice should contain FieldCurrentModel")
	}
	if snapAll.GetInt(FieldTurnCount) != 10 {
		t.Errorf("TopicAll slice should contain FieldTurnCount")
	}
}

func TestStateStore_History(t *testing.T) {
	store := NewStateStore(WithHistorySize(10))

	for i := 0; i < 5; i++ {
		store.Set(FieldTurnCount, i)
	}

	// Get all history.
	history := store.History(0)
	if len(history) != 5 {
		t.Fatalf("History(0) returned %d entries, want 5", len(history))
	}

	// Check ordering (oldest to newest).
	for i, h := range history {
		if h.NewValue != i {
			t.Errorf("history[%d].NewValue = %v, want %d", i, h.NewValue, i)
		}
	}

	// Get last 3.
	last3 := store.History(3)
	if len(last3) != 3 {
		t.Fatalf("History(3) returned %d entries, want 3", len(last3))
	}
	if last3[0].NewValue != 2 {
		t.Errorf("History(3)[0].NewValue = %v, want 2", last3[0].NewValue)
	}
}

func TestStateStore_History_RingBuffer(t *testing.T) {
	store := NewStateStore(WithHistorySize(4))

	// Write more than buffer size.
	for i := 0; i < 10; i++ {
		store.Set(FieldTurnCount, i)
	}

	// Should only have last 4.
	history := store.History(0)
	if len(history) != 4 {
		t.Fatalf("History(0) returned %d entries, want 4", len(history))
	}

	// Oldest should be 6, newest should be 9.
	if history[0].NewValue != 6 {
		t.Errorf("oldest in history = %v, want 6", history[0].NewValue)
	}
	if history[3].NewValue != 9 {
		t.Errorf("newest in history = %v, want 9", history[3].NewValue)
	}
}

func TestStateStore_HistoryForTopic(t *testing.T) {
	store := NewStateStore(WithHistorySize(20))

	store.Set(FieldCurrentModel, "m1")
	store.Set(FieldTurnCount, 1)
	store.Set(FieldCurrentModel, "m2")
	store.Set(FieldInputTokens, 100)
	store.Set(FieldCurrentModel, "m3")

	// Only model changes.
	modelHistory := store.HistoryForTopic(TopicModel, 0)
	if len(modelHistory) != 3 {
		t.Fatalf("HistoryForTopic(TopicModel) = %d entries, want 3", len(modelHistory))
	}
	for _, h := range modelHistory {
		if h.Topic != TopicModel {
			t.Errorf("entry has topic %q, want %q", h.Topic, TopicModel)
		}
	}

	// Limit to 2.
	limited := store.HistoryForTopic(TopicModel, 2)
	if len(limited) != 2 {
		t.Fatalf("HistoryForTopic(TopicModel, 2) = %d entries, want 2", len(limited))
	}
}

// --- Observer tests ---

func TestObserver_ReceivesChanges(t *testing.T) {
	store := NewStateStore()
	obs := store.Subscribe(TopicModel)
	defer obs.Close()

	store.Set(FieldCurrentModel, "claude-3")

	select {
	case change := <-obs.Changes():
		if change.Field != FieldCurrentModel {
			t.Errorf("received change.Field = %q, want %q", change.Field, FieldCurrentModel)
		}
		if change.NewValue != "claude-3" {
			t.Errorf("received change.NewValue = %v, want %q", change.NewValue, "claude-3")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for change")
	}
}

func TestObserver_TopicFiltering(t *testing.T) {
	store := NewStateStore()
	obs := store.Subscribe(TopicModel)
	defer obs.Close()

	// This change is for TopicConversation, should NOT be received.
	store.Set(FieldTurnCount, 1)

	// This change is for TopicModel, should be received.
	store.Set(FieldCurrentModel, "claude-3")

	select {
	case change := <-obs.Changes():
		if change.Field != FieldCurrentModel {
			t.Errorf("first received change.Field = %q, want %q (topic filtering failed)", change.Field, FieldCurrentModel)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for model change")
	}
}

func TestObserver_AllTopics(t *testing.T) {
	store := NewStateStore()
	obs := store.Subscribe() // no topics = all topics
	defer obs.Close()

	store.Set(FieldCurrentModel, "m1")
	store.Set(FieldTurnCount, 1)

	var received []StateChange
	timeout := time.After(100 * time.Millisecond)
	for {
		select {
		case c := <-obs.Changes():
			received = append(received, c)
			if len(received) == 2 {
				goto done
			}
		case <-timeout:
			goto done
		}
	}
done:
	if len(received) != 2 {
		t.Fatalf("received %d changes, want 2", len(received))
	}
}

func TestObserver_MultipleTopics(t *testing.T) {
	store := NewStateStore()
	obs := store.Subscribe(TopicModel, TopicTokens)
	defer obs.Close()

	store.Set(FieldCurrentModel, "m1")    // TopicModel - should receive
	store.Set(FieldTurnCount, 1)          // TopicConversation - should NOT receive
	store.Set(FieldInputTokens, 100)      // TopicTokens - should receive
	store.Set(FieldPermissionMode, "ask") // TopicPermission - should NOT receive

	var received []StateChange
	timeout := time.After(100 * time.Millisecond)
	for {
		select {
		case c := <-obs.Changes():
			received = append(received, c)
			if len(received) == 2 {
				goto done
			}
		case <-timeout:
			goto done
		}
	}
done:
	if len(received) != 2 {
		t.Fatalf("received %d changes, want 2", len(received))
	}
	topics := map[Topic]bool{}
	for _, c := range received {
		topics[c.Topic] = true
	}
	if !topics[TopicModel] || !topics[TopicTokens] {
		t.Errorf("expected TopicModel and TopicTokens, got %v", topics)
	}
}

func TestObserver_Close(t *testing.T) {
	store := NewStateStore()
	obs := store.Subscribe(TopicModel)

	if store.ObserverCount() != 1 {
		t.Fatalf("observer count = %d, want 1", store.ObserverCount())
	}

	obs.Close()

	if store.ObserverCount() != 0 {
		t.Fatalf("observer count after Close = %d, want 0", store.ObserverCount())
	}

	// Channel should be closed.
	_, ok := <-obs.Changes()
	if ok {
		t.Error("channel should be closed after Close()")
	}

	// Double close should not panic.
	obs.Close()
}

func TestObserver_NonBlocking(t *testing.T) {
	store := NewStateStore(WithObserverBuffer(2))
	obs := store.Subscribe(TopicModel)
	defer obs.Close()

	// Fill the buffer.
	store.Set(FieldCurrentModel, "m1")
	store.Set(FieldCurrentModel, "m2")

	// This should be dropped (non-blocking).
	store.Set(FieldCurrentModel, "m3")
	store.Set(FieldCurrentModel, "m4")

	// Give a tiny bit of time for sends to complete.
	time.Sleep(10 * time.Millisecond)

	if dropped := obs.Dropped(); dropped != 2 {
		t.Errorf("Dropped() = %d, want 2", dropped)
	}
}

func TestObserver_BatchNotification(t *testing.T) {
	store := NewStateStore()
	obs := store.Subscribe(TopicModel, TopicConversation)
	defer obs.Close()

	// Batch update with fields from different topics.
	store.Update(map[StateField]any{
		FieldCurrentModel:       "claude-3",
		FieldConversationStatus: StatusThinking,
	})

	var received []StateChange
	timeout := time.After(100 * time.Millisecond)
	for {
		select {
		case c := <-obs.Changes():
			received = append(received, c)
			if len(received) == 2 {
				goto done
			}
		case <-timeout:
			goto done
		}
	}
done:
	if len(received) != 2 {
		t.Fatalf("received %d changes from batch, want 2", len(received))
	}
}

// --- Client registry tests ---

func TestClientRegistry_RegisterUnregister(t *testing.T) {
	store := NewStateStore()

	client := store.RegisterClient(RegisterClientOpts{
		ID:     "client-1",
		Type:   ClientTypeTUI,
		Name:   "Terminal",
		Topics: []Topic{TopicConversation, TopicModel},
	})

	if client == nil {
		t.Fatal("RegisterClient returned nil")
		return
	}
	if client.ID != "client-1" {
		t.Errorf("client.ID = %q, want %q", client.ID, "client-1")
	}
	if client.Type != ClientTypeTUI {
		t.Errorf("client.Type = %q, want %q", client.Type, ClientTypeTUI)
	}
	if client.GetStatus() != ClientStatusActive {
		t.Errorf("client.Status = %q, want %q", client.GetStatus(), ClientStatusActive)
	}
	if store.ClientCount() != 1 {
		t.Errorf("ClientCount = %d, want 1", store.ClientCount())
	}
	if store.ObserverCount() != 1 {
		t.Errorf("ObserverCount = %d, want 1", store.ObserverCount())
	}

	// Verify the client has an observer.
	if client.Observer() == nil {
		t.Fatal("client.Observer() is nil")
		return
	}

	// Unregister.
	store.UnregisterClient("client-1")
	if store.ClientCount() != 0 {
		t.Errorf("ClientCount after unregister = %d, want 0", store.ClientCount())
	}
	if store.ObserverCount() != 0 {
		t.Errorf("ObserverCount after unregister = %d, want 0", store.ObserverCount())
	}
	if client.GetStatus() != ClientStatusDisconnected {
		t.Errorf("client.Status after unregister = %q, want %q", client.GetStatus(), ClientStatusDisconnected)
	}
}

func TestClientRegistry_GetClient(t *testing.T) {
	store := NewStateStore()

	store.RegisterClient(RegisterClientOpts{
		ID:   "c1",
		Type: ClientTypeTUI,
	})
	store.RegisterClient(RegisterClientOpts{
		ID:   "c2",
		Type: ClientTypeACP,
	})

	c1 := store.GetClient("c1")
	if c1 == nil || c1.ID != "c1" {
		t.Error("GetClient(c1) failed")
	}

	c2 := store.GetClient("c2")
	if c2 == nil || c2.ID != "c2" {
		t.Error("GetClient(c2) failed")
	}

	if store.GetClient("nonexistent") != nil {
		t.Error("GetClient(nonexistent) should return nil")
	}
}

func TestClientRegistry_ListClients(t *testing.T) {
	store := NewStateStore()

	store.RegisterClient(RegisterClientOpts{ID: "c1", Type: ClientTypeTUI})
	store.RegisterClient(RegisterClientOpts{ID: "c2", Type: ClientTypeACP})
	store.RegisterClient(RegisterClientOpts{ID: "c3", Type: ClientTypeIDE})

	clients := store.ListClients()
	if len(clients) != 3 {
		t.Fatalf("ListClients returned %d, want 3", len(clients))
	}
}

func TestClientRegistry_Touch(t *testing.T) {
	store := NewStateStore()

	client := store.RegisterClient(RegisterClientOpts{
		ID:   "c1",
		Type: ClientTypeTUI,
	})

	originalLastSeen := client.LastSeen
	time.Sleep(1 * time.Millisecond)
	client.Touch()

	if !client.LastSeen.After(originalLastSeen) {
		t.Error("Touch did not update LastSeen")
	}
}

func TestClientRegistry_HealthCheck(t *testing.T) {
	store := NewStateStore()

	client := store.RegisterClient(RegisterClientOpts{
		ID:   "c1",
		Type: ClientTypeTUI,
	})

	// Set LastSeen to the past.
	client.mu.Lock()
	client.LastSeen = time.Now().Add(-2 * time.Second)
	client.mu.Unlock()

	stale := store.CheckClientHealth(1 * time.Second)
	if len(stale) != 1 {
		t.Fatalf("CheckClientHealth found %d stale, want 1", len(stale))
	}
	if stale[0].ID != "c1" {
		t.Errorf("stale client ID = %q, want %q", stale[0].ID, "c1")
	}
	if client.GetStatus() != ClientStatusUnresponsive {
		t.Errorf("client status = %q, want %q", client.GetStatus(), ClientStatusUnresponsive)
	}
}

func TestClientRegistry_DisconnectStale(t *testing.T) {
	store := NewStateStore()

	c := store.RegisterClient(RegisterClientOpts{ID: "c1", Type: ClientTypeTUI})
	c.mu.Lock()
	c.LastSeen = time.Now().Add(-5 * time.Second)
	c.mu.Unlock()

	store.RegisterClient(RegisterClientOpts{ID: "c2", Type: ClientTypeACP})

	disconnected := store.DisconnectStaleClients(2 * time.Second)
	if len(disconnected) != 1 {
		t.Fatalf("DisconnectStaleClients returned %d, want 1", len(disconnected))
	}
	if disconnected[0] != "c1" {
		t.Errorf("disconnected[0] = %q, want %q", disconnected[0], "c1")
	}
	if store.ClientCount() != 1 {
		t.Errorf("ClientCount after disconnect = %d, want 1", store.ClientCount())
	}
}

func TestClientRegistry_UnregisterNonexistent(t *testing.T) {
	store := NewStateStore()
	// Should not panic.
	store.UnregisterClient("nonexistent")
}

// --- Concurrency tests ---

func TestConcurrent_ReadWrite(t *testing.T) {
	store := NewStateStore()
	const goroutines = 50
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Writers.
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				store.Set(FieldTurnCount, id*iterations+i)
			}
		}(g)
	}

	// Readers.
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_ = store.GetInt(FieldTurnCount)
				_ = store.Snapshot()
			}
		}()
	}

	wg.Wait()

	// Version should be goroutines * iterations.
	expectedVersion := uint64(goroutines * iterations)
	if v := store.Version(); v != expectedVersion {
		t.Errorf("version = %d, want %d", v, expectedVersion)
	}
}

func TestConcurrent_Observers(t *testing.T) {
	store := NewStateStore(WithObserverBuffer(32))
	const numObservers = 20
	const numWrites = 50

	observers := make([]*Observer, numObservers)
	for i := 0; i < numObservers; i++ {
		observers[i] = store.Subscribe(TopicConversation)
	}

	var wg sync.WaitGroup
	wg.Add(1)

	// Writer goroutine.
	go func() {
		defer wg.Done()
		for i := 0; i < numWrites; i++ {
			store.Set(FieldTurnCount, i)
		}
	}()

	wg.Wait()

	// Give time for channel delivery.
	time.Sleep(50 * time.Millisecond)

	// Each observer should have received some changes (may have drops if buffer fills).
	for i, obs := range observers {
		received := 0
		for {
			select {
			case <-obs.Changes():
				received++
			default:
				goto counted
			}
		}
	counted:
		total := received + int(obs.Dropped())
		if total != numWrites {
			t.Errorf("observer[%d]: received=%d + dropped=%d = %d, want %d",
				i, received, obs.Dropped(), total, numWrites)
		}
		obs.Close()
	}
}

func TestConcurrent_SubscribeUnsubscribe(t *testing.T) {
	store := NewStateStore()
	const goroutines = 30
	const iterations = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				obs := store.Subscribe(TopicModel)
				store.Set(FieldCurrentModel, fmt.Sprintf("model-%d", i))
				obs.Close()
			}
		}()
	}

	wg.Wait()

	if count := store.ObserverCount(); count != 0 {
		t.Errorf("observer count after all Close = %d, want 0", count)
	}
}

func TestConcurrent_BatchUpdate(t *testing.T) {
	store := NewStateStore()
	const goroutines = 20
	const iterations = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				store.Update(map[StateField]any{
					FieldTurnCount:    id*iterations + i,
					FieldInputTokens:  (id*iterations + i) * 10,
					FieldOutputTokens: (id*iterations + i) * 5,
				})
			}
		}(g)
	}

	wg.Wait()

	// Version increments 3 per batch (one per field).
	expectedVersion := uint64(goroutines * iterations * 3)
	if v := store.Version(); v != expectedVersion {
		t.Errorf("version = %d, want %d", v, expectedVersion)
	}
}

func TestConcurrent_ClientRegistry(t *testing.T) {
	store := NewStateStore()
	const goroutines = 20

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Register clients concurrently.
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			store.RegisterClient(RegisterClientOpts{
				ID:   fmt.Sprintf("client-%d", id),
				Type: ClientTypeTUI,
			})
		}(g)
	}

	// Simultaneously query clients.
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			_ = store.ListClients()
			_ = store.ClientCount()
		}()
	}

	wg.Wait()

	if count := store.ClientCount(); count != goroutines {
		t.Errorf("ClientCount = %d, want %d", count, goroutines)
	}

	// Unregister all concurrently.
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			store.UnregisterClient(fmt.Sprintf("client-%d", id))
		}(g)
	}
	wg.Wait()

	if count := store.ClientCount(); count != 0 {
		t.Errorf("ClientCount after unregister = %d, want 0", count)
	}
	if count := store.ObserverCount(); count != 0 {
		t.Errorf("ObserverCount after unregister = %d, want 0", count)
	}
}

func TestConcurrent_Stress(t *testing.T) {
	store := NewStateStore(WithHistorySize(64), WithObserverBuffer(16))
	const writers = 10
	const readers = 10
	const subscriberChurn = 10
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(writers + readers + subscriberChurn)

	// Writers: rapidly update state.
	for g := 0; g < writers; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				store.Update(map[StateField]any{
					FieldTurnCount:          id*iterations + i,
					FieldConversationStatus: StatusThinking,
				})
			}
		}(g)
	}

	// Readers: rapidly snapshot and read history.
	for g := 0; g < readers; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				snap := store.Snapshot()
				_ = snap.GetInt(FieldTurnCount)
				_ = store.History(10)
				_ = store.HistoryForTopic(TopicConversation, 5)
			}
		}()
	}

	// Subscriber churn: create and destroy observers rapidly.
	for g := 0; g < subscriberChurn; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				obs := store.Subscribe(TopicConversation)
				// Drain a few.
				for j := 0; j < 3; j++ {
					select {
					case <-obs.Changes():
					default:
					}
				}
				obs.Close()
			}
		}()
	}

	wg.Wait()

	// Verify no observers leaked.
	if count := store.ObserverCount(); count != 0 {
		t.Errorf("observer count after stress = %d, want 0", count)
	}
}

// --- Topic tests ---

func TestTopicForField(t *testing.T) {
	tests := []struct {
		field StateField
		want  Topic
	}{
		{FieldConversationStatus, TopicConversation},
		{FieldCurrentModel, TopicModel},
		{FieldFallbackModel, TopicModel},
		{FieldSessionID, TopicSession},
		{FieldInputTokens, TopicTokens},
		{FieldActiveTools, TopicTools},
		{FieldPermissionMode, TopicPermission},
		{FieldConnectedClients, TopicClients},
		{FieldActiveAgents, TopicAgents},
		{FieldTurnCount, TopicConversation},
		{FieldIsCompacting, TopicConversation},
	}

	for _, tt := range tests {
		if got := TopicForField(tt.field); got != tt.want {
			t.Errorf("TopicForField(%q) = %q, want %q", tt.field, got, tt.want)
		}
	}
}

func TestTopicForField_Unknown(t *testing.T) {
	got := TopicForField("nonexistent_field")
	if got != TopicAll {
		t.Errorf("TopicForField(unknown) = %q, want %q", got, TopicAll)
	}
}

func TestAllTopics(t *testing.T) {
	topics := AllTopics()
	if len(topics) != 8 {
		t.Errorf("AllTopics() returned %d topics, want 8", len(topics))
	}
	// Ensure TopicAll is NOT in the list.
	for _, topic := range topics {
		if topic == TopicAll {
			t.Error("AllTopics() should not contain TopicAll")
		}
	}
}

// --- StateSnapshot tests ---

func TestStateSnapshot_Get(t *testing.T) {
	snap := &StateSnapshot{
		Fields: map[StateField]any{
			FieldCurrentModel: "claude-3",
			FieldTurnCount:    42,
			FieldIsCompacting: true,
		},
	}

	if snap.Get(FieldCurrentModel) != "claude-3" {
		t.Error("Get failed for string field")
	}
	if snap.GetString(FieldCurrentModel) != "claude-3" {
		t.Error("GetString failed")
	}
	if snap.GetInt(FieldTurnCount) != 42 {
		t.Error("GetInt failed")
	}
	if snap.GetBool(FieldIsCompacting) != true {
		t.Error("GetBool failed")
	}

	// Non-existent fields.
	if snap.Get(FieldSessionID) != nil {
		t.Error("Get should return nil for missing field")
	}
	if snap.GetString(FieldSessionID) != "" {
		t.Error("GetString should return empty for missing field")
	}
	if snap.GetInt(FieldSessionID) != 0 {
		t.Error("GetInt should return 0 for missing field")
	}
	if snap.GetBool(FieldSessionID) != false {
		t.Error("GetBool should return false for missing field")
	}
}

func TestStateSnapshot_NilFields(t *testing.T) {
	snap := &StateSnapshot{}
	if snap.Get(FieldCurrentModel) != nil {
		t.Error("Get on nil Fields should return nil")
	}
}

// --- Observer ID test ---

func TestObserver_ID(t *testing.T) {
	store := NewStateStore()
	obs := store.SubscribeWithID("my-observer", TopicModel)
	defer obs.Close()

	if obs.ID() != "my-observer" {
		t.Errorf("observer ID = %q, want %q", obs.ID(), "my-observer")
	}
}

// --- Client observer receives state changes ---

func TestClient_ReceivesStateChanges(t *testing.T) {
	store := NewStateStore()

	client := store.RegisterClient(RegisterClientOpts{
		ID:     "c1",
		Type:   ClientTypeTUI,
		Topics: []Topic{TopicModel},
	})

	store.Set(FieldCurrentModel, "claude-3")

	obs := client.Observer()
	select {
	case change := <-obs.Changes():
		if change.Field != FieldCurrentModel {
			t.Errorf("client received change for %q, want %q", change.Field, FieldCurrentModel)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("client did not receive state change")
	}
}

func TestClient_FilteredByTopics(t *testing.T) {
	store := NewStateStore()

	client := store.RegisterClient(RegisterClientOpts{
		ID:     "c1",
		Type:   ClientTypeTUI,
		Topics: []Topic{TopicModel},
	})

	// Set a conversation field (should not be received by this client).
	store.Set(FieldTurnCount, 5)

	obs := client.Observer()
	select {
	case change := <-obs.Changes():
		// The FieldConnectedClients change from RegisterClient might arrive.
		if change.Topic == TopicConversation {
			t.Error("client should not receive TopicConversation changes")
		}
	case <-time.After(50 * time.Millisecond):
		// Expected: no change received.
	}
}

// --- WithOptions tests ---

func TestWithHistorySize(t *testing.T) {
	store := NewStateStore(WithHistorySize(5))

	for i := 0; i < 10; i++ {
		store.Set(FieldTurnCount, i)
	}

	history := store.History(0)
	if len(history) != 5 {
		t.Errorf("history size = %d, want 5", len(history))
	}
}

func TestWithObserverBuffer(t *testing.T) {
	store := NewStateStore(WithObserverBuffer(3))
	obs := store.Subscribe(TopicModel)
	defer obs.Close()

	// Fill buffer.
	for i := 0; i < 3; i++ {
		store.Set(FieldCurrentModel, fmt.Sprintf("m%d", i))
	}

	// Next should be dropped.
	store.Set(FieldCurrentModel, "m3")
	time.Sleep(10 * time.Millisecond)

	if obs.Dropped() != 1 {
		t.Errorf("Dropped() = %d, want 1", obs.Dropped())
	}
}
