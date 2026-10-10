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

// readers are kept per connection: http.ReadRequest buffers ahead, so a fresh
// bufio.Reader on every request would lose bytes belonging to the next one.
var (
	readerMu sync.Mutex
	readers  = map[net.Conn]*bufio.Reader{}
)

// dropReader forgets a connection's reader once the tunnel is closed, or
// the map would hold a reader and its connection for the proxy's life.
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

// connResponse is an http.ResponseWriter that writes an HTTP/1.1 response
// directly onto a hijacked, TLS-terminated connection. net/http will not do
// this for us: inside a CONNECT tunnel we are both the server and the
// transport.
type connResponse struct {
	conn   net.Conn
	header http.Header
	closed bool
	wrote  bool
	// last says the answer went out with no length, so it ends where the
	// connection does and the tunnel must close behind it
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

	// an answer with no length ends where the connection does (http.Error
	// writes one), and the tunnel is otherwise held open for the next
	// request: the client would wait out the tunnel's idle minute for the
	// end of a one-line body. Say this one closes, and close it (tunnel)
	if c.Header().Get("Content-Length") == "" && bodyAllowed(status) {
		c.Header().Set("Connection", "close")
		c.last = true
	}

	var b strings.Builder
	fmt.Fprintf(&b, "HTTP/1.1 %d %s\r\n", status, http.StatusText(status))

	// deterministic header order keeps a tcpdump of a failing run readable
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
