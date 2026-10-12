package replayproxy

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
)

// readers are kept per connection: a fresh reader per request would lose the bytes already buffered for the next.
var (
	readerMu sync.Mutex
	readers  = map[net.Conn]*bufio.Reader{}
)

// dropReader forgets a closed tunnel's reader.
func dropReader(c net.Conn) {
	readerMu.Lock()
	defer readerMu.Unlock()
	delete(readers, c)
}

func newReader(c net.Conn) *bufio.Reader {
	readerMu.Lock()
	defer readerMu.Unlock()

	r, ok := readers[c]
	if !ok {
		r = bufio.NewReader(c)
		readers[c] = r
	}

	return r
}

// connResponse writes an HTTP/1.1 answer straight onto a hijacked connection: inside a tunnel the proxy is both server and transport.
type connResponse struct {
	conn   net.Conn
	header http.Header
	closed bool
	wrote  bool
	// last is an answer with no length, which ends where the connection does, so the tunnel must close behind it
	last bool
}

func (c *connResponse) Header() http.Header {
	if c.header == nil {
		c.header = http.Header{}
	}

	return c.header
}

func (c *connResponse) WriteHeader(status int) {
	if c.wrote {
		return
	}
	c.wrote = true

	// an answer with no length ends where the connection does, so say this one closes, or the client waits out the idle minute for it
	if c.Header().Get("Content-Length") == "" && bodyAllowed(status) {
		c.Header().Set("Connection", "close")
		c.last = true
	}

	var b strings.Builder
	fmt.Fprintf(&b, "HTTP/1.1 %d %s\r\n", status, http.StatusText(status))

	// a fixed header order keeps a capture of a failing run readable
	names := make([]string, 0, len(c.Header()))
	for name := range c.Header() {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		for _, v := range c.Header()[name] {
			fmt.Fprintf(&b, "%s: %s\r\n", name, v)
		}
	}
	b.WriteString("\r\n")

	if _, err := c.conn.Write([]byte(b.String())); err != nil {
		c.closed = true
	}
}

// bodyAllowed reports whether an answer with this status can carry a body.
func bodyAllowed(status int) bool {
	return status >= http.StatusOK && status != http.StatusNoContent && status != http.StatusNotModified
}

func (c *connResponse) Write(p []byte) (int, error) {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	n, err := c.conn.Write(p)
	if err != nil {
		c.closed = true
	}

	return n, err
}
