package tools

import (
	"context"
	"strconv"

	"github.com/katbyte/go-kt/lock"
	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
)

// The calls of one turn are kept from undoing each other with go-kt's locks.
// The MCP server runs a turn's calls at once, and an edit of a series reads
// the record and sends it back whole: two series_edit calls each adding a tag
// to one series would each send the tags as they were before the other, and
// one tag would stay. The same goes for a season's flag and the series' own.
//
// An edit locks the series it has resolved, and only then reads the record it
// will write back. The locks are a set of this server's own (registry.locks),
// shared by every session it serves; Sonarr's web app, or another sonarr-mcp,
// is not held back.

// tagsLock is the lock on the tags, which a tag made on the way to an edit
// reads the list of before adding to it: two calls making the same new tag
// would each find it missing.
const tagsLock = "tags"

// seriesLock is the series with this id, for an edit that has resolved it and
// not yet read the record it will send back.
func seriesLock(id sonarr.SeriesID) lock.Thing {
	return lock.ID[sonarr.SeriesResource](strconv.Itoa(int(id)))
}

// locking is this server's set of locks, made when the first is asked for.
func (r *registry) locking() *lock.Set {
	r.locksOnce.Do(func() { r.locks = lock.NewSet() })

	return r.locks
}

// makeTags resolves tags by label or id, making the ones that are not there,
// with the tags held for the length of it. It is for a caller that holds no
// lock of its own: one that does names tagsLock beside what else it locks,
// since a caller locks once at a time.
func (r *registry) makeTags(ctx context.Context, refs []string) ([]sonarr.TagID, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	unlock := r.locking().ByString(tagsLock)
	defer unlock()

	return r.resolveTags(ctx, refs, true)
}
