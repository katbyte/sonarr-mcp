package providerproxy

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
