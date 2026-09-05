package signalbox

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

type StoreConfig struct {
	Now             func() time.Time
	DefaultSettings Settings
	MaxActors       uint32
}

type actorKey struct {
	hostID  string
	worldID string
	actorID string
}

type inbox struct {
	settings       Settings
	signals        []Signal
	nextCursor     uint64
	lastKindMillis map[string]int64
}

type Store struct {
	mu              sync.RWMutex
	now             func() time.Time
	defaultSettings Settings
	maxActors       uint32
	inboxes         map[actorKey]*inbox
	globalSequence  uint64
	changed         chan struct{}
	closed          bool
	blocked         error
	durable         *sqliteInboxStore
}

func NewStore(config StoreConfig) (*Store, error) {
	now := config.Now
	if now == nil {
		now = time.Now
	}
	settings := config.DefaultSettings
	if settings == (Settings{}) {
		settings = DefaultSettings()
	}
	if err := ValidateSettings(settings); err != nil {
		return nil, err
	}
	maxActors := config.MaxActors
	if maxActors == 0 {
		maxActors = 4_096
	}
	if maxActors > 100_000 {
		return nil, invalid("max_actors", "must not exceed 100000")
	}
	return &Store{
		now: now, defaultSettings: settings, maxActors: maxActors,
		inboxes: make(map[actorKey]*inbox), changed: make(chan struct{}),
	}, nil
}

func (store *Store) Configure(target Target, settings Settings) (Settings, error) {
	if err := ValidateTarget(target); err != nil {
		return Settings{}, err
	}
	if err := ValidateSettings(settings); err != nil {
		return Settings{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.readyLocked(); err != nil {
		return Settings{}, err
	}
	box, err := store.inboxLocked(keyOf(target), true)
	if err != nil {
		return Settings{}, err
	}
	unchanged := box.settings == settings
	box.settings = settings
	if !settings.Enabled {
		for i := range box.signals {
			if box.signals[i].Delivery.Status == "" || box.signals[i].Delivery.Status == "retry" {
				box.signals[i].Delivery.Status = "dropped"
				box.signals[i].Delivery.Reason = "inbox-disabled"
				box.signals[i].Delivery.RetryAtUnixMillis = 0
			}
		}
	}
	store.pruneLocked(box, store.now().UnixMilli())
	if len(box.signals) > int(settings.MaxPending) {
		box.signals = append([]Signal(nil), box.signals[len(box.signals)-int(settings.MaxPending):]...)
	}
	if !unchanged || (store.durable != nil && store.durable.bytes[keyOf(target)] == 0) {
		if err := store.persistInboxLocked(keyOf(target), box); err != nil {
			return Settings{}, err
		}
	}
	if !unchanged {
		store.notifyLocked()
	}
	return settings, nil
}

func (store *Store) Settings(target Target) (Settings, error) {
	if err := ValidateTarget(target); err != nil {
		return Settings{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	if err := store.readyLocked(); err != nil {
		return Settings{}, err
	}
	if box := store.inboxes[keyOf(target)]; box != nil {
		return box.settings, nil
	}
	return store.defaultSettings, nil
}

func (store *Store) Publish(value Signal) (PublishResult, error) {
	now := store.now().UnixMilli()
	if err := validateSignal(value, now); err != nil {
		return PublishResult{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.readyLocked(); err != nil {
		return PublishResult{}, err
	}
	box, err := store.inboxLocked(keyOf(Target{value.HostID, value.WorldID, value.ActorID}), true)
	if err != nil {
		return PublishResult{}, err
	}
	store.pruneLocked(box, now)
	if !box.settings.Enabled {
		return PublishResult{Reason: "disabled"}, nil
	}
	for _, existing := range box.signals {
		if existing.SignalID == value.SignalID {
			return PublishResult{Reason: "duplicate", Cursor: existing.Cursor}, nil
		}
	}
	if last := box.lastKindMillis[value.Kind]; last != 0 &&
		now-last < int64(box.settings.CooldownMillis) {
		return PublishResult{Reason: "cooldown"}, nil
	}
	if len(box.signals) >= int(box.settings.MaxPending) {
		return PublishResult{Reason: "capacity"}, nil
	}
	if _, known := box.lastKindMillis[value.Kind]; !known && len(box.lastKindMillis) >= 1024 {
		return PublishResult{Reason: "capacity"}, nil
	}
	if box.nextCursor >= 1<<53-1 || store.globalSequence >= 1<<53-1 {
		return PublishResult{}, ErrInvalid
	}
	box.nextCursor++
	store.globalSequence++
	value.SchemaVersion = SchemaVersion
	value.Summary = strings.TrimSpace(value.Summary)
	value.ReceivedAtUnixMillis = now
	value.Cursor = box.nextCursor
	value.globalSequence = store.globalSequence
	box.signals = append(box.signals, value)
	box.lastKindMillis[value.Kind] = now
	if err := store.persistInboxLocked(keyOf(Target{value.HostID, value.WorldID, value.ActorID}), box); err != nil {
		return PublishResult{}, err
	}
	store.notifyLocked()
	return PublishResult{Accepted: true, Cursor: value.Cursor}, nil
}

func (store *Store) List(input ListInput) (Page, error) {
	input, err := validateList(input)
	if err != nil {
		return Page{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.readyLocked(); err != nil {
		return Page{}, err
	}
	box := store.inboxes[keyOf(input.Target)]
	if box == nil {
		if input.AfterCursor != 0 {
			return Page{}, ErrInvalid
		}
		return Page{Signals: []Signal{}}, nil
	}
	store.pruneLocked(box, store.now().UnixMilli())
	if input.AfterCursor > box.nextCursor {
		return Page{}, ErrInvalid
	}
	return pageLocked(box, input.AfterCursor, input.Limit), nil
}

func (store *Store) Wait(ctx context.Context, input WaitInput) (Update, error) {
	if input.WaitMillis > 25_000 {
		return Update{}, invalid("wait_millis", "must not exceed 25000")
	}
	input.ListInput.Limit = boundedLimit(input.ListInput.Limit)
	timer := time.NewTimer(time.Duration(input.WaitMillis) * time.Millisecond)
	defer timer.Stop()
	for {
		page, changed, wake, err := store.pageAndWake(input.ListInput)
		if err != nil {
			return Update{}, err
		}
		if changed || input.WaitMillis == 0 {
			return Update{Changed: changed, Page: page}, nil
		}
		select {
		case <-ctx.Done():
			return Update{}, ctx.Err()
		case <-timer.C:
			page, changed, _, err = store.pageAndWake(input.ListInput)
			return Update{Changed: changed, Page: page}, err
		case <-wake:
		}
	}
}

// WaitAny is the process-local internal scheduler feed. It is intentionally
// not exposed through MCP or HTTP.
func (store *Store) WaitAny(ctx context.Context, after uint64, wait time.Duration) ([]Signal, uint64, error) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		store.mu.Lock()
		if err := store.readyLocked(); err != nil {
			store.mu.Unlock()
			return nil, after, err
		}
		if after > store.globalSequence {
			store.mu.Unlock()
			return nil, after, ErrInvalid
		}
		now := store.now().UnixMilli()
		items := make([]Signal, 0)
		for _, box := range store.inboxes {
			store.pruneLocked(box, now)
			for _, item := range box.signals {
				if item.globalSequence > after {
					items = append(items, item)
				}
			}
		}
		sort.Slice(items, func(left, right int) bool {
			return items[left].globalSequence < items[right].globalSequence
		})
		latest, wake := store.globalSequence, store.changed
		store.mu.Unlock()
		if len(items) != 0 || wait == 0 {
			return cloneSignals(items), latest, nil
		}
		select {
		case <-ctx.Done():
			return nil, after, ctx.Err()
		case <-timer.C:
			return []Signal{}, latest, nil
		case <-wake:
		}
	}
}

func (store *Store) Close() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.closed {
		store.closed = true
		store.notifyLocked()
		if store.durable != nil {
			return store.durable.close()
		}
	}
	return nil
}

func (store *Store) pageAndWake(input ListInput) (Page, bool, <-chan struct{}, error) {
	validated, err := validateList(input)
	if err != nil {
		return Page{}, false, nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.readyLocked(); err != nil {
		return Page{}, false, nil, err
	}
	box := store.inboxes[keyOf(validated.Target)]
	if box == nil {
		if validated.AfterCursor != 0 {
			return Page{}, false, nil, ErrInvalid
		}
		return Page{Signals: []Signal{}}, false, store.changed, nil
	}
	store.pruneLocked(box, store.now().UnixMilli())
	if validated.AfterCursor > box.nextCursor {
		return Page{}, false, nil, ErrInvalid
	}
	page := pageLocked(box, validated.AfterCursor, validated.Limit)
	return page, len(page.Signals) != 0, store.changed, nil
}

func (store *Store) inboxLocked(key actorKey, create bool) (*inbox, error) {
	if box := store.inboxes[key]; box != nil {
		return box, nil
	}
	if !create {
		return nil, ErrNotFound
	}
	if len(store.inboxes) >= int(store.maxActors) {
		return nil, invalid("actors", "capacity reached")
	}
	box := &inbox{
		settings: store.defaultSettings, lastKindMillis: make(map[string]int64),
	}
	store.inboxes[key] = box
	return box, nil
}

func (store *Store) pruneLocked(box *inbox, now int64) {
	kept := box.signals[:0]
	for _, item := range box.signals {
		if item.ExpiresAtUnixMillis > now {
			kept = append(kept, item)
		}
	}
	box.signals = kept
	for kind, at := range box.lastKindMillis {
		if now-at >= int64(box.settings.CooldownMillis) {
			delete(box.lastKindMillis, kind)
		}
	}
}

func (store *Store) notifyLocked() {
	close(store.changed)
	store.changed = make(chan struct{})
}

func pageLocked(box *inbox, after uint64, limit uint32) Page {
	items := make([]Signal, 0, limit)
	more := false
	for _, item := range box.signals {
		if item.Cursor <= after {
			continue
		}
		if len(items) == int(limit) {
			more = true
			break
		}
		items = append(items, item)
	}
	next := after
	if len(items) != 0 {
		next = items[len(items)-1].Cursor
	}
	return Page{Signals: cloneSignals(items), NextCursor: next, More: more}
}

func cloneSignals(items []Signal) []Signal {
	result := append([]Signal(nil), items...)
	for index := range result {
		result[index].globalSequence = 0
	}
	return result
}

func keyOf(target Target) actorKey {
	return actorKey{hostID: target.HostID, worldID: target.WorldID, actorID: target.ActorID}
}

func boundedLimit(value uint32) uint32 {
	if value == 0 {
		return 32
	}
	return value
}
