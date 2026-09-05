package host

import (
	"bytes"
	"fmt"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func cacheTestDocument(n int) []byte {
	return []byte(fmt.Sprintf(`{"$schema":"https://json-schema.org/draft/2020-12/schema","additionalProperties":false,"properties":{"name":{"minLength":%d,"type":"string"}},"required":["name"],"type":"object"}`, n))
}

func TestSchemaCacheConcurrentReuseAndEviction(t *testing.T) {
	cache := schemaCache{capacity: 2}
	const workers = 16
	results := make(chan *jsonschema.Schema, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			compiled, err := cache.compile(cacheTestDocument(1))
			if err != nil {
				t.Error(err)
				return
			}
			if err := compiled.Validate(map[string]any{"name": "ok"}); err != nil {
				t.Error(err)
			}
			if err := compiled.Validate(map[string]any{"name": ""}); err == nil {
				t.Error("invalid instance accepted")
			}
			results <- compiled
		}()
	}
	wg.Wait()
	close(results)
	var first *jsonschema.Schema
	for result := range results {
		if first == nil {
			first = result
		} else if first != result {
			t.Fatal("concurrent callers did not reuse compilation")
		}
	}
	if first == nil {
		t.Fatal("no compiled result")
	}
	for i := 2; i <= 3; i++ {
		if _, err := cache.compile(cacheTestDocument(i)); err != nil {
			t.Fatal(err)
		}
	}
	if len(cache.entries) != 2 || cache.entries[string(cacheTestDocument(1))] != nil {
		t.Fatal("cache did not evict oldest schema")
	}
	if err := first.Validate(map[string]any{"name": "ok"}); err != nil {
		t.Fatalf("eviction broke in-flight schema: %v", err)
	}
	again, err := cache.compile(cacheTestDocument(1))
	if err != nil || again == first {
		t.Fatalf("evicted schema was not recompiled: %v", err)
	}
	// Failed compilations must not displace useful entries.
	if _, err := cache.compile([]byte(`{"type":"invalid"}`)); err == nil {
		t.Fatal("invalid schema compiled")
	}
	if len(cache.entries) != 2 {
		t.Fatal("failed compilation changed cache size")
	}
}

func TestWarmSchemaCacheStillChecksDocumentAndDigest(t *testing.T) {
	schema, err := NewSchema(cacheTestDocument(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.ValidateInstance([]byte(`{"name":"a"}`)); err != nil {
		t.Fatal(err)
	}
	changed := schema
	changed.Document = bytes.ReplaceAll(schema.Document, []byte(`"minLength":1`), []byte(`"minLength":2`))
	if err := changed.Validate(); err == nil {
		t.Fatal("warm cache trusted stale digest")
	}
	changed.SHA256 = sha256Hex(changed.Document)
	if err := changed.ValidateInstance([]byte(`{"name":"a"}`)); err == nil {
		t.Fatal("changed document reused old constraints")
	}
	if err := schema.ValidateInstance([]byte(`{"name":"a"}`)); err != nil {
		t.Fatal("new schema changed existing entry")
	}
	noncanonical := schema
	noncanonical.Document = append([]byte(" "), schema.Document...)
	noncanonical.SHA256 = sha256Hex(noncanonical.Document)
	if err := noncanonical.Validate(); err == nil {
		t.Fatal("warm cache accepted noncanonical document")
	}
}

func BenchmarkSchemaCompilation(b *testing.B) {
	document := cacheTestDocument(1)
	b.Run("uncached", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := compileSchemaUncached(document); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("cached", func(b *testing.B) {
		cache := schemaCache{capacity: maxCompiledSchemas}
		if _, err := cache.compile(document); err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := cache.compile(document); err != nil {
				b.Fatal(err)
			}
		}
	})
}
