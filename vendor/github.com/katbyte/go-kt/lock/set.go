package lock

import (
	"cmp"
	"slices"
	"strings"
	"sync"
)

// Set is a set of locks of its own: what is locked in one set holds nobody
// up in another, All included. An application that serves one world from
// one process can use the package's own By, ByString and All, which share
// one set; one that holds several, or a test that wants to see nothing was
// left locked, makes a set with NewSet. The zero value is ready to use.
//
// A set keeps a lock for a key only while somebody holds or waits for it, so
// a long-running server does not keep one for every record it ever touched.
type Set struct {
	all sync.RWMutex

	mu   sync.Mutex
	keys map[Key]*keyLock
}

type keyLock struct {
	sync.Mutex

	holders int
}

// NewSet returns a set of locks that shares nothing with any other.
func NewSet() *Set {
	return &Set{}
}

// By locks every thing named, in this set, until unlock is called (see the
// package's By).
func (s *Set) By(things ...Thing) (unlock func()) {
	keys := make([]Key, 0, len(things))
	for _, t := range things {
		keys = append(keys, keyOf(t))
	}
	slices.SortFunc(keys, func(a, b Key) int {
		return cmp.Or(strings.Compare(a.kind, b.kind), strings.Compare(a.id, b.id), strings.Compare(a.field, b.field))
	})
	keys = slices.Compact(keys)

	s.all.RLock()
	held := make([]*keyLock, 0, len(keys))
	for _, key := range keys {
		s.mu.Lock()
		if s.keys == nil {
			s.keys = map[Key]*keyLock{}
		}
		k := s.keys[key]
		if k == nil {
			k = &keyLock{}
			s.keys[key] = k
		}
		k.holders++
		s.mu.Unlock()

		k.Lock()
		held = append(held, k)
	}

	return func() {
		for i, k := range slices.Backward(held) {
			k.Unlock()
			s.mu.Lock()
			k.holders--
			if k.holders == 0 {
				delete(s.keys, keys[i])
			}
			s.mu.Unlock()
		}
		s.all.RUnlock()
	}
}

// ByString locks the thing known by this string, in this set.
func (s *Set) ByString(key string) (unlock func()) {
	return s.By(String(key))
}

// All locks everything there is or could be in this set: it waits for every
// lock in it to be unlocked, and none is granted until it is.
func (s *Set) All() (unlock func()) {
	s.all.Lock()

	return s.all.Unlock
}

// Idle reports whether nothing in the set is locked and nobody waits for
// anything in it: what a test asks once every call it made has returned, to
// see that none left a lock behind.
func (s *Set) Idle() bool {
	if !s.all.TryLock() {
		return false
	}
	defer s.all.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.keys) == 0
}
