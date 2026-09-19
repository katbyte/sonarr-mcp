package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/sonarr-mcp/lib/client"
	"github.com/katbyte/sonarr-mcp/lib/sonarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// providerSettings are a provider's plain settings - its URL, host, category
// - with the credentials left out: Sonarr marks each field's privacy, and only
// the ordinary ones are returned.
func providerSettings(fields []sonarr.Field) map[string]any {
	out := map[string]any{}
	for _, f := range fields {
		if f.Privacy != "" && f.Privacy != sonarr.PrivacyLevelNormal {
			continue
		}
		switch v := f.Value.(type) {
		case nil:
		case string:
			if v != "" {
				out[f.Name] = v
			}
		case []any:
			if len(v) > 0 {
				out[f.Name] = v
			}
		default:
			out[f.Name] = v
		}
	}

	return out
}

// testRow is the outcome of testing one provider.
type testRow struct {
	Name     string   `json:"name"`
	OK       bool     `json:"ok"`
	Problems []string `json:"problems" jsonschema:"what Sonarr found wrong, when it failed"`
}

// testResults names the outcome of testing every provider of a kind.
func testResults(results []sonarr.ProviderTestAllResult, names map[int]string) []testRow {
	out := make([]testRow, 0, len(results))
	for _, res := range results {
		row := testRow{Name: names[res.Id], OK: boolv(res.IsValid)}
		if row.Name == "" {
			row.Name = fmt.Sprintf("id %d", res.Id)
		}
		for _, f := range res.ValidationFailures {
			msg := f.ErrorMessage
			if f.PropertyName != "" {
				msg = f.PropertyName + ": " + msg
			}
			row.Problems = append(row.Problems, msg)
		}
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b testRow) int { return strings.Compare(a.Name, b.Name) })

	return out
}

// testOne is the outcome of testing one provider: a validation failure is an
// answer, not an error.
func testOne(name string, err error) (testRow, error) {
	row := testRow{Name: name, OK: err == nil}
	if err == nil {
		return row, nil
	}
	se, ok := errors.AsType[*client.StatusError](err)
	if !ok {
		return row, err
	}
	row.Problems = se.Messages
	if len(row.Problems) == 0 {
		row.Problems = []string{se.Body}
	}

	return row, nil
}

func registerProviderTools(r *registry) {
	sc := r.client

	type indexerRow struct {
		ID                int            `json:"id"`
		Name              string         `json:"name"`
		Implementation    string         `json:"implementation"`
		Protocol          string         `json:"protocol"`
		RSS               bool           `json:"rss"`
		AutomaticSearch   bool           `json:"automatic_search"`
		InteractiveSearch bool           `json:"interactive_search"`
		Priority          int            `json:"priority"`
		Tags              []string       `json:"tags"`
		Settings          map[string]any `json:"settings"           jsonschema:"its URL, categories and the rest, without credentials"`
	}
	type indexersOut struct {
		Indexers []indexerRow `json:"indexers"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "indexer_list",
		Description: "The indexers Sonarr searches: each one's kind, whether it is used for RSS, automatic and interactive search, its priority, tags and settings (without credentials).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, indexersOut, error) {
		res, err := sc.GetIndexer(ctx)
		if err != nil {
			return nil, indexersOut{}, err
		}
		tags, err := r.tagLabels(ctx)
		if err != nil {
			return nil, indexersOut{}, err
		}
		out := indexersOut{}
		for _, i := range res.Model {
			out.Indexers = append(out.Indexers, indexerRow{
				ID: i.Id, Name: i.Name, Implementation: i.Implementation, Protocol: string(i.Protocol),
				RSS: boolv(i.EnableRss), AutomaticSearch: boolv(i.EnableAutomaticSearch), InteractiveSearch: boolv(i.EnableInteractiveSearch),
				Priority: i.Priority, Tags: labels(i.Tags, tags), Settings: providerSettings(i.Fields),
			})
		}

		return nil, out, nil
	})

	type testIn struct {
		Name string `json:"name,omitempty" jsonschema:"one to test, by name; default every enabled one"`
	}
	type testOut struct {
		Results []testRow `json:"results"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "indexer_test",
		Description: "Test indexers the way Sonarr's Test button does - reach it, authenticate, run a query - one by name or every enabled one, and say what is wrong with any that fail. Changes nothing.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in testIn) (*mcp.CallToolResult, testOut, error) {
		res, err := sc.GetIndexer(ctx)
		if err != nil {
			return nil, testOut{}, err
		}
		names := map[int]string{}
		for _, i := range res.Model {
			names[i.Id] = i.Name
		}
		if in.Name == "" {
			// Sonarr answers 400 when any fails, with every result all the
			// same; the SDK takes that as an answer, not an error
			all, testErr := sc.PostIndexerTestAll(ctx)
			if testErr != nil {
				return nil, testOut{}, testErr
			}
			return nil, testOut{Results: testResults(all.Model, names)}, nil
		}
		i := slices.IndexFunc(res.Model, func(x sonarr.IndexerResource) bool { return strings.EqualFold(x.Name, in.Name) })
		if i < 0 {
			return nil, testOut{}, fmt.Errorf("no indexer %q (have: %s)", in.Name, strings.Join(mapValues(names), ", "))
		}
		_, err = sc.PostIndexerTest(ctx, res.Model[i], sonarr.PostIndexerTestOperationOptions{ForceTest: new(true)})
		row, err := testOne(res.Model[i].Name, err)

		return nil, testOut{Results: []testRow{row}}, err
	})

	type clientRow struct {
		ID              int            `json:"id"`
		Name            string         `json:"name"`
		Implementation  string         `json:"implementation"`
		Protocol        string         `json:"protocol"`
		Enabled         bool           `json:"enabled"`
		Priority        int            `json:"priority"`
		RemoveCompleted bool           `json:"remove_completed" jsonschema:"Sonarr removes downloads from the client once imported"`
		RemoveFailed    bool           `json:"remove_failed"`
		Tags            []string       `json:"tags"`
		Settings        map[string]any `json:"settings"         jsonschema:"its host, port, category and the rest, without credentials"`
	}
	type clientsOut struct {
		Clients []clientRow `json:"download_clients"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "downloadclient_list",
		Description: "The download clients Sonarr sends releases to: each one's kind, whether it is enabled, its priority, whether Sonarr removes finished and failed downloads from it, its tags and settings (without credentials).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, clientsOut, error) {
		res, err := sc.GetDownloadClient(ctx)
		if err != nil {
			return nil, clientsOut{}, err
		}
		tags, err := r.tagLabels(ctx)
		if err != nil {
			return nil, clientsOut{}, err
		}
		out := clientsOut{}
		for _, c := range res.Model {
			out.Clients = append(out.Clients, clientRow{
				ID: c.Id, Name: c.Name, Implementation: c.Implementation, Protocol: string(c.Protocol), Enabled: boolv(c.Enable),
				Priority: c.Priority, RemoveCompleted: boolv(c.RemoveCompletedDownloads), RemoveFailed: boolv(c.RemoveFailedDownloads),
				Tags: labels(c.Tags, tags), Settings: providerSettings(c.Fields),
			})
		}

		return nil, out, nil
	})

	add(r, readTool, &mcp.Tool{
		Name:        "downloadclient_test",
		Description: "Test download clients the way Sonarr's Test button does - reach it, authenticate, check its category and settings - one by name or every enabled one, and say what is wrong with any that fail. Changes nothing.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in testIn) (*mcp.CallToolResult, testOut, error) {
		res, err := sc.GetDownloadClient(ctx)
		if err != nil {
			return nil, testOut{}, err
		}
		names := map[int]string{}
		for _, c := range res.Model {
			names[c.Id] = c.Name
		}
		if in.Name == "" {
			// Sonarr answers 400 when any fails, with every result all the
			// same; the SDK takes that as an answer, not an error
			all, testErr := sc.PostDownloadClientTestAll(ctx)
			if testErr != nil {
				return nil, testOut{}, testErr
			}
			return nil, testOut{Results: testResults(all.Model, names)}, nil
		}
		i := slices.IndexFunc(res.Model, func(x sonarr.DownloadClientResource) bool { return strings.EqualFold(x.Name, in.Name) })
		if i < 0 {
			return nil, testOut{}, fmt.Errorf("no download client %q (have: %s)", in.Name, strings.Join(mapValues(names), ", "))
		}
		_, err = sc.PostDownloadClientTest(ctx, res.Model[i], sonarr.PostDownloadClientTestOperationOptions{ForceTest: new(true)})
		row, err := testOne(res.Model[i].Name, err)

		return nil, testOut{Results: []testRow{row}}, err
	})
}

// mapValues lists a map's values, sorted.
func mapValues(m map[int]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	slices.Sort(out)

	return out
}
