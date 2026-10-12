// Package lock makes callers in one process take turns at a record, so one does not undo another's change.
//
// Two edits of one record at once lose one of them: each reads the whole record and sends it back, and the later undoes the earlier. An MCP client
// runs a turn's calls together, so eight calls each adding a tag left one. Holding the record from the read to the write keeps them apart:
//
//	unlock := lock.By(playlist)
//	defer unlock()
//
// By takes any mix in one call: a record, lock.ID[Item](id) for one known only by id, lock.Field(me, "bookmarks") for a piece of one, lock.String for
// a thing with no record. A record is locked by its type and id, never by the Go value, so a lockable type says which record it is with LockID()
// string. All locks everything, for a change that rewrites a whole collection.
//
// By, ByString and All share one set for the process. A process holding several worlds, two servers that each have an item 12, gives each a set of
// its own (NewSet), or puts the world in the key. Lock once, naming everything needed in that call: the keys are taken in one order so two callers
// cannot each hold what the other waits for, which a second call while holding the first gives up. These locks hold back nothing outside the process.
package lock

import (
	"reflect"
)

// process is the one set of locks every caller in the process shares.
var process Set

// Thing is what By locks: a value that says which record it is. The lock is on the type and the id, so two copies of one record are one thing and a
// playlist and a collection both numbered 12 are two.
type Thing interface {
	// LockID is the record's id, compared as written: a type whose server reads one id in several spellings folds them to one here.
	LockID() string
}

// Key describes a thing to lock when there is no value in hand: a record by id (ID), a piece of a thing (Field), or a string (String).
type Key struct {
	kind, id, field string
}

// LockID is what the key names, written out; a Key is itself a Thing.
func (k Key) LockID() string {
	s := k.id
	if k.kind != "" {
		s = k.kind + " " + s
	}
	if k.field != "" {
		s += " " + k.field
	}

	return s
}

// ID is the record of type T with this id, for a caller without the value: the same lock as By(item) when item.LockID() is id.
func ID[T any](id string) Key {
	return Key{kind: kindOf(reflect.TypeFor[T]()), id: id}
}

// Field is a named piece of a thing changed on its own, an account's bookmarks; a separate lock from the thing and its other fields.
func Field(of Thing, name string) Key {
	k := keyOf(of)
	if k.field != "" {
		name = k.field + "." + name
	}
	k.field = name

	return k
}

// String is a thing known only by a string of the caller's making; no record's lock, whatever it says.
func String(s string) Key {
	return Key{id: s}
}

// By locks every thing named until unlock is called. They are taken in one order, each once, so two callers with overlapping things cannot wait on
// each other for ever. Naming nothing locks nothing.
func By(things ...Thing) (unlock func()) {
	return process.By(things...)
}

// ByString locks the thing known by this string: By(String(key)).
func ByString(key string) (unlock func()) {
	return process.By(String(key))
}

// All locks everything there is or could be: it waits for every other lock to go, and none is granted until it is unlocked.
func All() (unlock func()) {
	return process.All()
}

// keyOf is the key a thing is locked by: a Key as is, a value by its type and id.
func keyOf(t Thing) Key {
	if k, ok := t.(Key); ok {
		return k
	}
	if t == nil {
		panic("lock: a nil thing has no record to lock")
	}

	return Key{kind: kindOf(reflect.TypeOf(t)), id: t.LockID()}
}

// kindOf names a type the same for a value and a pointer to one, and apart from a namesake in another package.
func kindOf(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Name() == "" {
		return t.String()
	}

	return t.PkgPath() + "." + t.Name()
}
