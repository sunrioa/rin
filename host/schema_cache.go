package host

import (
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const maxCompiledSchemas = 128

var compiledSchemas = schemaCache{capacity: maxCompiledSchemas}

// schemaCache uses canonical document bytes, never a caller-supplied digest.
// FIFO eviction bounds retained entries. Compiled schemas remain private and
// immutable; evicting an entry does not affect an in-flight validation.
type schemaCache struct {
	mu       sync.Mutex
	capacity int
	entries  map[string]*jsonschema.Schema
	keys     []string
	next     int
}

func (cache *schemaCache) compile(canonical []byte) (*jsonschema.Schema, error) {
	key := string(canonical)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if compiled := cache.entries[key]; compiled != nil {
		return compiled, nil
	}
	// Serialize misses so concurrent requests for one schema compile it once.
	compiled, err := compileSchemaUncached(canonical)
	if err != nil {
		return nil, err
	}
	if cache.capacity <= 0 {
		return compiled, nil
	}
	if cache.entries == nil {
		cache.entries = make(map[string]*jsonschema.Schema)
	}
	if len(cache.keys) < cache.capacity {
		cache.keys = append(cache.keys, key)
	} else {
		delete(cache.entries, cache.keys[cache.next])
		cache.keys[cache.next] = key
		cache.next = (cache.next + 1) % cache.capacity
	}
	cache.entries[key] = compiled
	return compiled, nil
}
