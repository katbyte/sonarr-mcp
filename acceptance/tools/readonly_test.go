//go:build acceptance

package tools_test

import (
	"cmp"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/katbyte/go-kt/mcp/acctest"
	"github.com/katbyte/pandorest/client"
	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
	"github.com/katbyte/sonarr-mcp/tools"
)

// A tool that changes nothing must call nothing that changes Sonarr, or --read-only, which loads only those tools, could be got round
// through one of them. Every request the suite's client sends is recorded, and each one a read-only tool sent is looked up in the client's
// own table of operations, which says of each whether it changes nothing.
var readOnly = struct {
	mu sync.Mutex
	// calls is every request the client has sent
	calls client.Calls
	// tools are the tools that change nothing, and reads the operations that change nothing, by name
	tools, reads map[string]bool
	// seen counts the requests read-only tools sent, so a run that recorded none says so rather than passing
	seen int
	// wrote is each read-only tool that sent an operation that is not a read, with those operations
	wrote map[string][]string
	// excused are the operations in changesNothing that a read-only tool was seen to send
	excused map[string]bool
}{wrote: map[string][]string{}, excused: map[string]bool{}}

// changesNothing are the operations a read-only tool may send although they are not reads, each with why. A provider's test is a POST of
// its settings, which Sonarr tries the way its Test button does and saves nothing of.
var changesNothing = map[string]string{
	"PostIndexerTest":           "an indexer's settings tried, nothing saved",
	"PostIndexerTestAll":        "every indexer tried, nothing saved",
	"PostDownloadClientTest":    "a download client's settings tried, nothing saved",
	"PostDownloadClientTestAll": "every download client tried, nothing saved",
	"PostImportListTest":        "an import list's settings tried, nothing saved",
	"PostImportListTestAll":     "every import list tried, nothing saved",
	"PostNotificationTest":      "a notification's settings tried, nothing saved, though a test message is sent",
	"PostNotificationTestAll":   "every notification tried, nothing saved, though a test message is sent",
}

// learnReadOnly reads which tools change nothing and which operations are reads, before the first tool is called.
func learnReadOnly() error {
	listed, err := tools.Describe(tools.Options{Toolsets: []string{"all"}, EnableDelete: true})
	if err != nil {
		return err
	}
	readOnly.tools = map[string]bool{}
	for _, tool := range listed {
		if tool.Kind == "read" {
			readOnly.tools[tool.Name] = true
		}
	}

	readOnly.reads = map[string]bool{}
	for _, op := range sonarr.Operations {
		if op.Read {
			readOnly.reads[op.Name] = true
		}
	}
	if len(readOnly.tools) == 0 || len(readOnly.reads) == 0 {
		return fmt.Errorf("%d read-only tools and %d operations that change nothing: the check would prove nothing", len(readOnly.tools), len(readOnly.reads))
	}

	return nil
}

// sentSoFar is how many requests the client has sent, to tell a tool's own from the ones before it.
func sentSoFar() int { return len(readOnly.calls.Seen()) }

// checkReadOnly notes any operation a read-only tool sent since from that changes Sonarr. A request no operation names is judged by its
// method, which a GET alone passes.
func checkReadOnly(name string, from int) {
	if !readOnly.tools[name] {
		return
	}

	sent := readOnly.calls.Seen()[from:]
	readOnly.mu.Lock()
	defer readOnly.mu.Unlock()

	readOnly.seen += len(sent)
	for _, c := range sent {
		if readOnly.reads[c.Operation] || (c.Operation == "" && c.Method == http.MethodGet) {
			continue
		}
		if _, ok := changesNothing[c.Operation]; ok {
			readOnly.excused[c.Operation] = true

			continue
		}
		op := cmp.Or(c.Operation, c.Method+" "+c.Path)
		if !slices.Contains(readOnly.wrote[name], op) {
			readOnly.wrote[name] = append(readOnly.wrote[name], op)
		}
	}
}

// readOnlyReport says which read-only tools sent an operation that changes Sonarr, and whether any read-only tool was seen to send anything.
// A whole run also names each operation in changesNothing that no read-only tool sent, so the list cannot go stale.
func readOnlyReport() string {
	readOnly.mu.Lock()
	defer readOnly.mu.Unlock()

	var out strings.Builder
	if len(readOnly.wrote) > 0 {
		fmt.Fprintf(&out, "\n%d tool(s) that change nothing called an operation that changes Sonarr:\n", len(readOnly.wrote))
		for _, name := range slices.Sorted(maps.Keys(readOnly.wrote)) {
			fmt.Fprintf(&out, "  %s: %s\n", name, strings.Join(readOnly.wrote[name], ", "))
		}
		fmt.Fprintln(&out, "mark the tool a write, or have it read what it needs another way")
	}
	if readOnly.seen == 0 {
		fmt.Fprintln(&out, "\nno tool that changes nothing was seen to send a request, so nothing was checked")
	}
	if acctest.WholeRun() {
		var stale []string
		for _, op := range slices.Sorted(maps.Keys(changesNothing)) {
			if !readOnly.excused[op] {
				stale = append(stale, op)
			}
		}
		if len(stale) > 0 {
			fmt.Fprintf(&out, "\n%d operation(s) in changesNothing were sent by no tool that changes nothing: %s\ntake them out\n", len(stale), strings.Join(stale, ", "))
		}
	}

	return out.String()
}
