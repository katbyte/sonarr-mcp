// Package lock makes the callers in one process take turns at a thing, so
// one does not undo another's change to it.
//
// A tool that edits a record by reading it whole and sending it back - no
// server it talks to has a conditional update - loses a change when two such
// edits run at once: each sends back what it read, and the later one undoes
// the earlier. An MCP client runs the calls of one turn at once, so eight
// calls each adding a tag to one book left one tag. Locking the record for
// the whole read and write keeps them apart:
//
//	unlock := lock.By(playlist)
//	defer unlock()
//	// read the playlist again, change it, send it back
//
// By locks whatever it is handed, any mix of things in one call:
//
//	lock.By(item)                                 // this record
//	lock.By(lock.ID[Item](id))                    // the same record, with only its id in hand
//	lock.By(item, lock.Field(me, "bookmarks"))    // a record, and a named piece of another
//	lock.By(lock.String("server settings"))       // something with no record behind it
//
// A record is locked as its type and its id, never as the Go value: two
// callers each fetch their own copy of one record, and both copies must come
// to the same lock. So a type that can be locked says which record a value
// is, with one method:
//
//	func (it *Item) LockID() string { return it.ID }
//
// All locks everything, for a change that reads a whole collection and
// writes it back.
//
// # One set, or a set of your own
//
// By, ByString and All work on one set of locks that every caller in the
// process shares. That is right for a process that is one world: one server,
// where a record's id means the same thing to every caller. A process that
// holds several worlds - two servers that each have an item 12, or a test
// run of many fake servers - either says which world in the key, as
// ByString(server + " " + id) does, or gives each world a set of its own
// with NewSet, whose By, ByString and All reach no further than that set.
// A set of its own is the only way to keep one world's All from holding up
// the others.
//
// Another client of the same server, its own web app included, is never
// held back: these locks are this process's alone.
//
// # One call at a time
//
// A caller locks once at a time, naming everything it needs in that one
// call. Whatever is named is taken in one order, so two callers that want
// overlapping things cannot each hold what the other waits for. Locking a
// second time while holding the first gives that up, and can also wait on an
// All that is itself waiting for the first to be unlocked.
package lock

import (
	"reflect"
)

// process is the one set of locks every caller in the process shares.
var process Set

// Thing is what By locks: a value that says which record it is. The lock is
// on the value's type and that id, so two values of one type with one id
// are one thing, and a playlist and a collection that are both number 12
// are two.
type Thing interface {
	// LockID is the id of the record this value is a copy of. It is
	// compared as it is written, so a type whose server reads one id in
	// several spellings folds them to one here.
	LockID() string
}

// Key is a thing to lock that is described, where there is no value of it to
// hand over: a record by its id (ID), a named piece of a thing (Field), or a
// plain string (String).
type Key struct {
	kind, id, field string
}

// LockID is what the key names, written out, so a Key is itself a Thing.
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

// ID is the record of type T with this id, for a caller that has not fetched
// it: By(ID[Item](id)) and By(item) are one lock when item.LockID() is id.
func ID[T any](id string) Key {
	return Key{kind: kindOf(reflect.TypeFor[T]()), id: id}
}

// Field is a named piece of a thing that is changed on its own: an account's
// list of bookmarks, a server's settings. It is another lock from the thing
// itself, and from its other fields.
func Field(of Thing, name string) Key {
	k := keyOf(of)
	if k.field != "" {
		name = k.field + "." + name
	}
	k.field = name

	return k
}

// String is a thing known only by a string of the caller's own making. It is
// no record's lock, whatever the string says.
func String(s string) Key {
	return Key{id: s}
}

// By locks every thing named until unlock is called, waiting for whoever has
// any of them. They are taken in one order, each once however often it is
// named, so two callers locking overlapping things cannot wait on each other
// for ever. Naming nothing locks nothing.
func By(things ...Thing) (unlock func()) {
	return process.By(things...)
}

// ByString locks the thing known by this string: By(String(key)).
func ByString(key string) (unlock func()) {
	return process.By(String(key))
}

// All locks everything there is or could be, for a change that reads a whole
// collection and writes it back: it waits for every other lock to be
// unlocked, and none is granted until it is.
func All() (unlock func()) {
	return process.All()
}

// keyOf is the key a thing is locked by: a Key as it is, and a value as its
// type and the id it gives.
func keyOf(t Thing) Key {
	if k, ok := t.(Key); ok {
		return k
	}
	if t == nil {
		panic("lock: a nil thing has no record to lock")
	}

	return Key{kind: kindOf(reflect.TypeOf(t)), id: t.LockID()}
}

// kindOf names a type the same way whether a value or a pointer to one is in
// hand, and apart from a type of the same name in another package.
func kindOf(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Name() == "" {
		return t.String()
	}

	return t.PkgPath() + "." + t.Name()
}
