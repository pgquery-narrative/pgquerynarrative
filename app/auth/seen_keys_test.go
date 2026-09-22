package auth

import (
	"strconv"
	"testing"
	"time"
)

func TestSeenManagedKeysAreBoundedAndExpire(t *testing.T) {
	var s seenManagedKeys
	p := Principal{UserID: "k1", OrgID: "o", Role: RoleViewer}
	if _, ok := s.get("h"); ok {
		t.Fatal("empty cache returned a key")
	}
	s.put("h", p)
	if got, ok := s.get("h"); !ok || got != p {
		t.Fatalf("remembered key not returned: %v %v", got, ok)
	}
	s.mu.Lock()
	e := s.m["h"]
	e.expires = time.Now().Add(-time.Second)
	s.m["h"] = e
	s.mu.Unlock()
	if _, ok := s.get("h"); ok {
		t.Error("an expired key was returned")
	}

	// Full of live entries: a new key is not remembered, an existing one can still refresh.
	var full seenManagedKeys
	for i := 0; i < seenManagedKeyMax; i++ {
		full.put("k"+strconv.Itoa(i), p)
	}
	full.put("new", p)
	if _, ok := full.get("new"); ok {
		t.Error("the cache grew past its bound")
	}
	full.put("k0", Principal{UserID: "k0-again"})
	if got, _ := full.get("k0"); got.UserID != "k0-again" {
		t.Error("an existing key could not be refreshed when full")
	}
	// Full of expired entries: they are dropped to make room.
	full.mu.Lock()
	for k, e := range full.m {
		e.expires = time.Now().Add(-time.Second)
		full.m[k] = e
	}
	full.mu.Unlock()
	full.put("new", p)
	if _, ok := full.get("new"); !ok {
		t.Error("expired entries were not dropped to make room")
	}
}
