package tools

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// configSection is one section of Sonarr's settings, each a single record
// with an id of its own, read and written whole.
type configSection struct {
	name    string
	aliases []string
	// names are the settings the section has, by Sonarr's names for them
	names []string
	read  func(ctx context.Context) (map[string]any, error)
	// shape is a section as Sonarr takes it, from its JSON form: a value of
	// the wrong kind is refused here, before anything is sent
	shape func(settings map[string]any) (map[string]any, error)
	write func(ctx context.Context, settings map[string]any) error
}

// section describes a settings section from how the SDK reads and writes it.
func section[T any](name string, aliases []string, get func(ctx context.Context) (*T, error), put func(ctx context.Context, id string, body T) error) configSection {
	return configSection{
		name: name, aliases: aliases,
		names: slices.DeleteFunc(propertyNames(reflect.TypeFor[T]()), func(n string) bool { return n == "id" }),
		read: func(ctx context.Context) (map[string]any, error) {
			body, err := get(ctx)
			if err != nil {
				return nil, err
			}
			return asMap(body)
		},
		shape: func(settings map[string]any) (map[string]any, error) {
			body, err := fromMap[T](settings)
			if err != nil {
				return nil, err
			}
			return asMap(body)
		},
		write: func(ctx context.Context, settings map[string]any) error {
			body, err := fromMap[T](settings)
			if err != nil {
				return err
			}
			return put(ctx, strconv.Itoa(recordID(settings)), body)
		},
	}
}

// configSections are the sections config_get reads and config_edit changes.
func (r *registry) configSections() []configSection {
	c := r.client

	return []configSection{
		section("naming", nil, func(ctx context.Context) (*sonarr.NamingConfigResource, error) {
			res, err := c.GetConfigNaming(ctx)
			return res.Model, err
		}, func(ctx context.Context, id string, body sonarr.NamingConfigResource) error {
			_, err := c.PutConfigNamingById(ctx, id, body)
			return err
		}),
		section("mediamanagement", []string{"media_management", "media management"}, func(ctx context.Context) (*sonarr.MediaManagementConfigResource, error) {
			res, err := c.GetConfigMediaManagement(ctx)
			return res.Model, err
		}, func(ctx context.Context, id string, body sonarr.MediaManagementConfigResource) error {
			_, err := c.PutConfigMediaManagementById(ctx, id, body)
			return err
		}),
		section("host", nil, func(ctx context.Context) (*sonarr.HostConfigResource, error) {
			res, err := c.GetConfigHost(ctx)
			return res.Model, err
		}, func(ctx context.Context, id string, body sonarr.HostConfigResource) error {
			_, err := c.PutConfigHostById(ctx, id, body)
			return err
		}),
		section("ui", nil, func(ctx context.Context) (*sonarr.UiConfigResource, error) {
			res, err := c.GetConfigUi(ctx)
			return res.Model, err
		}, func(ctx context.Context, id string, body sonarr.UiConfigResource) error {
			_, err := c.PutConfigUiById(ctx, id, body)
			return err
		}),
		section("indexer", nil, func(ctx context.Context) (*sonarr.IndexerConfigResource, error) {
			res, err := c.GetConfigIndexer(ctx)
			return res.Model, err
		}, func(ctx context.Context, id string, body sonarr.IndexerConfigResource) error {
			_, err := c.PutConfigIndexerById(ctx, id, body)
			return err
		}),
		section("downloadclient", []string{"download_client", "download client"}, func(ctx context.Context) (*sonarr.DownloadClientConfigResource, error) {
			res, err := c.GetConfigDownloadClient(ctx)
			return res.Model, err
		}, func(ctx context.Context, id string, body sonarr.DownloadClientConfigResource) error {
			_, err := c.PutConfigDownloadClientById(ctx, id, body)
			return err
		}),
		section("importlist", []string{"import_list", "import list"}, func(ctx context.Context) (*sonarr.ImportListConfigResource, error) {
			res, err := c.GetConfigImportList(ctx)
			return res.Model, err
		}, func(ctx context.Context, id string, body sonarr.ImportListConfigResource) error {
			_, err := c.PutConfigImportListById(ctx, id, body)
			return err
		}),
	}
}

// configSectionNames says which sections there are, for a refusal and for
// the tools' own words.
const configSectionNames = "naming, mediamanagement, host, ui, indexer, downloadclient or importlist"

// findSection is the section a caller named.
func (r *registry) findSection(name string) (*configSection, error) {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, s := range r.configSections() {
		if s.name == want || slices.Contains(s.aliases, want) {
			return &s, nil
		}
	}

	return nil, fmt.Errorf("section must be %s, got %q", configSectionNames, name)
}

// withoutSecrets is a section without its credentials.
func withoutSecrets(settings map[string]any) map[string]any {
	out := maps.Clone(settings)
	maps.DeleteFunc(out, func(k string, _ any) bool { return secretSetting(k) })

	return out
}

// lockingOut are the host settings that can cut this server off from Sonarr:
// where it listens, and how it lets a caller in.
var lockingOut = []string{"port", "sslPort", "enableSsl", "bindAddress", "urlBase", "authenticationMethod", "authenticationRequired", "apiKey", "sslCertPath", "sslCertPassword"}

func registerConfigEdit(r *registry) {
	type editIn struct {
		Section string         `json:"section"           jsonschema:"naming, mediamanagement, host, ui, indexer, downloadclient or importlist"`
		Set     map[string]any `json:"set"               jsonschema:"the settings to change, by the names config_get shows, each with its new value"`
		Confirm bool           `json:"confirm,omitempty" jsonschema:"for the host section: true to change it; without it nothing changes and the answer says what would"`
	}
	type editOut struct {
		Section     string         `json:"section"`
		Changed     []string       `json:"changed"                jsonschema:"what changed, from and to; a credential is only said to be set"`
		WouldChange []string       `json:"would_change,omitempty" jsonschema:"for the host section without confirm: what a confirmed call changes; nothing has changed"`
		NotKept     []string       `json:"not_kept,omitempty"     jsonschema:"settings Sonarr accepted and did not keep: read back, they are not what was set"`
		Warning     string         `json:"warning,omitempty"`
		Settings    map[string]any `json:"settings"               jsonschema:"the section as Sonarr now holds it; credentials are left out"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "config_edit",
		Description: "Change one section of Sonarr's settings, by the names config_get shows: the episode and folder naming formats and whether files are renamed (naming); hardlinks, the recycling bin, permissions, what happens to deleted and unmonitored files (mediamanagement); retention and RSS interval (indexer); completed and failed download handling (downloadclient); list sync (importlist); the web interface's dates and language (ui); and the host's port, address, URL base, sign-in, proxy, logging, updates and backups (host). Only the settings given change. Host settings can cut this server off from Sonarr, so they change nothing without confirm.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		sec, err := r.findSection(in.Section)
		if err != nil {
			return nil, editOut{}, err
		}
		if len(in.Set) == 0 {
			return nil, editOut{}, fmt.Errorf("nothing to change: give set, with any of %s", strings.Join(sec.names, ", "))
		}
		cur, err := sec.read(ctx)
		if err != nil {
			return nil, editOut{}, err
		}

		next := maps.Clone(cur)
		var asked []string
		for _, key := range slices.Sorted(maps.Keys(in.Set)) {
			i := slices.IndexFunc(sec.names, func(n string) bool { return fold(n) == fold(key) })
			if i < 0 {
				return nil, editOut{}, fmt.Errorf("the %s section has no setting %q (it has: %s)", sec.name, key, strings.Join(sec.names, ", "))
			}
			next[sec.names[i]] = in.Set[key]
			asked = append(asked, sec.names[i])
		}
		shaped, err := sec.shape(next)
		if err != nil {
			return nil, editOut{}, err
		}
		was, err := sec.shape(cur)
		if err != nil {
			return nil, editOut{}, err
		}

		out := editOut{Section: sec.name, Settings: withoutSecrets(cur)}
		changed := recordChanges(was, shaped)
		if len(changed) == 0 {
			return nil, out, nil
		}
		if sec.name == "host" {
			if cuts := slices.DeleteFunc(slices.Clone(asked), func(n string) bool { return !slices.Contains(lockingOut, n) }); len(cuts) > 0 {
				out.Warning = fmt.Sprintf("changing %s changes where Sonarr listens or how it lets a caller in: this server may not reach it afterwards until its own address and key are changed to match, and some take a restart to apply", strings.Join(cuts, ", "))
			}
			if !in.Confirm {
				out.WouldChange = changed

				return nil, out, nil
			}
		}

		shaped["id"] = cur["id"]
		if err := sec.write(ctx, shaped); err != nil {
			return nil, editOut{}, err
		}
		out.Changed = changed

		// Sonarr takes some settings and does not keep them (a username while
		// nothing asks anyone to sign in), so the section is read back and
		// what did not hold is said, rather than a change reported that did
		// not happen
		after, err := sec.read(ctx)
		if err != nil {
			return nil, out, err
		}
		for _, name := range asked {
			if !secretSetting(name) && !reflect.DeepEqual(after[name], shaped[name]) {
				out.NotKept = append(out.NotKept, fmt.Sprintf("%s: set to %s, and Sonarr holds %s", name, shown(shaped[name]), shown(after[name])))
			}
		}
		out.Settings = withoutSecrets(after)

		return nil, out, nil
	})
}
