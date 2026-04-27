package server

import (
	"sync"
	"testing"
)

// TestRegisterSession_DisplacesPriorWithSameKey covers the duplicate-WS
// path: Lit re-mount or HMR or a connectGateway race opens two
// near-simultaneous connections with the same client instanceId. The
// older session should be evicted from the map and shutdown invoked.
func TestRegisterSession_DisplacesPriorWithSameKey(t *testing.T) {
	srv := &Server{sessions: make(map[string]*Session)}
	a := &Session{server: srv, connID: "AAA"}
	b := &Session{server: srv, connID: "BBB"}

	if displaced := srv.registerSession("clientid|tabA", a); displaced != nil {
		t.Errorf("first register should not displace anything, got %+v", displaced)
	}
	if got := srv.sessions["clientid|tabA"]; got != a {
		t.Errorf("after first register, map should have a, got %+v", got)
	}
	if displaced := srv.registerSession("clientid|tabA", b); displaced != a {
		t.Errorf("second register should displace a, got %+v", displaced)
	}
	if got := srv.sessions["clientid|tabA"]; got != b {
		t.Errorf("after second register, map should have b, got %+v", got)
	}
}

// TestRegisterSession_SeparateInstanceIDsCoexist covers the multi-tab
// case: two real, distinct UI tabs have different instanceIds and
// should not knock each other off.
func TestRegisterSession_SeparateInstanceIDsCoexist(t *testing.T) {
	srv := &Server{sessions: make(map[string]*Session)}
	a := &Session{server: srv}
	b := &Session{server: srv}

	srv.registerSession("clientid|tabA", a)
	srv.registerSession("clientid|tabB", b)

	if srv.sessions["clientid|tabA"] != a || srv.sessions["clientid|tabB"] != b {
		t.Errorf("two tabs should coexist: %+v", srv.sessions)
	}
}

// TestUnregisterSession_CASIgnoresStaleEntry locks in the
// compare-and-delete behavior: the displaced session deregistering on its
// way out must not delete its successor.
func TestUnregisterSession_CASIgnoresStaleEntry(t *testing.T) {
	srv := &Server{sessions: make(map[string]*Session)}
	a := &Session{server: srv}
	b := &Session{server: srv}

	srv.registerSession("k", a)
	srv.registerSession("k", b) // displaces a; map now has b

	// Simulate a's deferred deregister. Even though the key is the same,
	// the value under it is now b, so the delete must NOT remove b.
	srv.unregisterSession("k", a)

	if got := srv.sessions["k"]; got != b {
		t.Errorf("displaced session deregistered the successor: got %+v", got)
	}

	// And b's own deregister DOES remove it.
	srv.unregisterSession("k", b)
	if _, ok := srv.sessions["k"]; ok {
		t.Errorf("deregister of the active session should remove it")
	}
}

// TestRegisterSession_EmptyKeyIsNoOp protects the case where the connect
// handshake didn't supply a clientId+instanceId pair (older clients,
// CLI-style connections); we should not register such sessions and
// definitely not collapse them all under "" → "".
func TestRegisterSession_EmptyKeyIsNoOp(t *testing.T) {
	srv := &Server{sessions: make(map[string]*Session)}
	a := &Session{server: srv}
	if displaced := srv.registerSession("", a); displaced != nil {
		t.Errorf("empty-key register should be a no-op")
	}
	if len(srv.sessions) != 0 {
		t.Errorf("empty-key register polluted map: %+v", srv.sessions)
	}
}

// TestRegisterSession_ConcurrentDoesntDeadlock is a smoke test for the
// mutex; running 100 goroutines hammering the same key should not panic
// or hang. Whichever goroutine wins the final register stays in the map.
func TestRegisterSession_ConcurrentDoesntDeadlock(t *testing.T) {
	srv := &Server{sessions: make(map[string]*Session)}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := &Session{server: srv}
			srv.registerSession("hammered", s)
			srv.unregisterSession("hammered", s)
		}()
	}
	wg.Wait()
}
