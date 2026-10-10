package tools

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
)

// Sonarr's settings, as the records it keeps them in: each kind below gets
// its list, add, edit, delete and test from registerRecords (records.go).
// What is here is only how the SDK reads and writes each kind, and what the
// tools say of themselves.

// specMaps are a kind's blank conditions as their JSON form.
func specMaps[S any](blanks []S) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(blanks))
	for i := range blanks {
		m, err := asMap(blanks[i])
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}

	return out, nil
}

func registerSettingsTools(r *registry) {
	c := r.client

	// indexers and download clients are listed and tested by tools of their
	// own (providers.go), with their credentials left out the same way
	registerRecords(r, recordKind[sonarr.IndexerResource]{
		prefix: "indexer", one: "an indexer", many: "indexers", nameKey: "name",
		loses: "its settings and its API key or login, which Sonarr does not keep once it is gone",
		list: func(ctx context.Context) ([]sonarr.IndexerResource, error) {
			res, err := c.GetIndexer(ctx)
			return res.Model, err
		},
		kinds: func(ctx context.Context) ([]sonarr.IndexerResource, error) {
			res, err := c.GetIndexerSchema(ctx)
			return res.Model, err
		},
		create: func(ctx context.Context, rec sonarr.IndexerResource, force bool) (*sonarr.IndexerResource, error) {
			res, err := c.PostIndexer(ctx, rec, sonarr.PostIndexerOperationOptions{ForceSave: new(force)})
			return res.Model, err
		},
		update: func(ctx context.Context, id int, rec sonarr.IndexerResource, force bool) (*sonarr.IndexerResource, error) {
			res, err := c.PutIndexerById(ctx, id, rec, sonarr.PutIndexerByIdOperationOptions{ForceSave: new(force)})
			return res.Model, err
		},
		remove: func(ctx context.Context, id int) error {
			_, err := c.DeleteIndexerById(ctx, id)
			return err
		},
		test: func(ctx context.Context, rec sonarr.IndexerResource) error {
			_, err := c.PostIndexerTest(ctx, rec, sonarr.PostIndexerTestOperationOptions{ForceTest: new(true)})
			return err
		},
		// as Sonarr's own page does: used for whatever the kind supports
		on: switchedOn("enableRss", "enableAutomaticSearch", "enableInteractiveSearch"),
		defaults: func(blank map[string]any) {
			blank["enableRss"] = blank["supportsRss"]
			blank["enableAutomaticSearch"], blank["enableInteractiveSearch"] = blank["supportsSearch"], blank["supportsSearch"]
		},
		kindsSays:  "The kinds of indexer Sonarr can add - Newznab, Torznab, and the trackers and feeds it knows by name - each with the settings it takes: the name, what it is for, its default and its choices. indexer_add takes one as kind.",
		addSays:    "Add an indexer for Sonarr to search: its kind (indexer_kinds lists them), a name, its own settings by name (baseUrl, apiKey, categories), and its properties with set (enableRss, enableAutomaticSearch and enableInteractiveSearch, each on unless set false where the kind supports it; priority; tags). Sonarr tries it first and refuses one that fails, unless force is set.",
		editSays:   "Change an indexer: its properties with set (name, enableRss, enableAutomaticSearch, enableInteractiveSearch, priority, tags) and its own settings by name (baseUrl, apiKey, categories). Only what is given changes. Sonarr tries it before saving unless force is set.",
		deleteSays: "Delete an indexer from Sonarr, with its settings and credentials. Nothing changes without confirm: the answer says what a confirmed call would delete. To stop Sonarr using one and keep it, indexer_edit it with enableRss and the two searches false.",
	})

	registerRecords(r, recordKind[sonarr.DownloadClientResource]{
		prefix: "downloadclient", one: "a download client", many: "download clients", nameKey: "name",
		loses: "its settings and its API key or login, which Sonarr does not keep once it is gone",
		list: func(ctx context.Context) ([]sonarr.DownloadClientResource, error) {
			res, err := c.GetDownloadClient(ctx)
			return res.Model, err
		},
		kinds: func(ctx context.Context) ([]sonarr.DownloadClientResource, error) {
			res, err := c.GetDownloadClientSchema(ctx)
			return res.Model, err
		},
		create: func(ctx context.Context, rec sonarr.DownloadClientResource, force bool) (*sonarr.DownloadClientResource, error) {
			res, err := c.PostDownloadClient(ctx, rec, sonarr.PostDownloadClientOperationOptions{ForceSave: new(force)})
			return res.Model, err
		},
		update: func(ctx context.Context, id int, rec sonarr.DownloadClientResource, force bool) (*sonarr.DownloadClientResource, error) {
			res, err := c.PutDownloadClientById(ctx, id, rec, sonarr.PutDownloadClientByIdOperationOptions{ForceSave: new(force)})
			return res.Model, err
		},
		remove: func(ctx context.Context, id int) error {
			_, err := c.DeleteDownloadClientById(ctx, id)
			return err
		},
		test: func(ctx context.Context, rec sonarr.DownloadClientResource) error {
			_, err := c.PostDownloadClientTest(ctx, rec, sonarr.PostDownloadClientTestOperationOptions{ForceTest: new(true)})
			return err
		},
		on:         switchedOn("enable"),
		defaults:   func(blank map[string]any) { blank["enable"] = true },
		kindsSays:  "The kinds of download client Sonarr can add - SABnzbd, NZBGet, qBittorrent, Transmission, Deluge, the blackholes and the rest - each with the settings it takes. downloadclient_add takes one as kind.",
		addSays:    "Add a download client for Sonarr to send releases to: its kind (downloadclient_kinds lists them), a name, its own settings by name (host, port, apiKey, tvCategory), and its properties with set (enable, on unless set false; priority; removeCompletedDownloads; removeFailedDownloads; tags). Sonarr tries it first and refuses one that fails, unless force is set.",
		editSays:   "Change a download client: its properties with set (name, enable, priority, removeCompletedDownloads, removeFailedDownloads, tags) and its own settings by name (host, port, apiKey, tvCategory). Only what is given changes. Sonarr tries it before saving unless force is set.",
		deleteSays: "Delete a download client from Sonarr, with its settings and credentials; downloads it holds are no longer tracked. Nothing changes without confirm: the answer says what a confirmed call would delete. To stop Sonarr using one and keep it, downloadclient_edit it with enable false.",
	})

	registerRecords(r, recordKind[sonarr.NotificationResource]{
		prefix: "notification", one: "a notification", many: "notifications", nameKey: "name",
		loses: "its settings and its webhook address, token or login, which Sonarr does not keep once it is gone",
		list: func(ctx context.Context) ([]sonarr.NotificationResource, error) {
			res, err := c.GetNotification(ctx)
			return res.Model, err
		},
		kinds: func(ctx context.Context) ([]sonarr.NotificationResource, error) {
			res, err := c.GetNotificationSchema(ctx)
			return res.Model, err
		},
		create: func(ctx context.Context, rec sonarr.NotificationResource, force bool) (*sonarr.NotificationResource, error) {
			res, err := c.PostNotification(ctx, rec, sonarr.PostNotificationOperationOptions{ForceSave: new(force)})
			return res.Model, err
		},
		update: func(ctx context.Context, id int, rec sonarr.NotificationResource, force bool) (*sonarr.NotificationResource, error) {
			res, err := c.PutNotificationById(ctx, id, rec, sonarr.PutNotificationByIdOperationOptions{ForceSave: new(force)})
			return res.Model, err
		},
		remove: func(ctx context.Context, id int) error {
			_, err := c.DeleteNotificationById(ctx, id)
			return err
		},
		test: func(ctx context.Context, rec sonarr.NotificationResource) error {
			_, err := c.PostNotificationTest(ctx, rec, sonarr.PostNotificationTestOperationOptions{ForceTest: new(true)})
			return err
		},
		testAll: func(ctx context.Context) ([]sonarr.ProviderTestAllResult, error) {
			res, err := c.PostNotificationTestAll(ctx)
			return res.Model, err
		},
		// every event it is sent for
		on: func(rec map[string]any) []string {
			return slices.DeleteFunc(slices.Sorted(maps.Keys(rec)), func(name string) bool { return !strings.HasPrefix(name, "on") || !asBool(rec[name]) })
		},
		listSays:   "The notifications (Sonarr calls them connections): where it sends word of a grab, an import, an upgrade, a rename, a health issue - Discord, email, a webhook, Plex, Kodi and the rest - each with what it is sent for (the on... properties), its tags and its settings without credentials.",
		kindsSays:  "The kinds of notification Sonarr can add - Discord, Email, Webhook, Telegram, Pushover, Plex, Emby, Kodi, a custom script and the rest - each with the settings it takes. notification_add takes one as kind.",
		addSays:    "Add a notification: its kind (notification_kinds lists them), a name, its own settings by name (webHookUrl for Discord, url for Webhook), and with set what it is sent for (onGrab, onDownload, onUpgrade, onRename, onSeriesAdd, onSeriesDelete, onEpisodeFileDelete, onHealthIssue, onHealthRestored, onManualInteractionRequired, onApplicationUpdate), each off unless set true, and its tags. Sonarr sends a test through one that is sent for something first and refuses one that fails, unless force is set; one sent for nothing is saved untried and does nothing.",
		editSays:   "Change a notification: with set its name, what it is sent for (the on... properties) and its tags, and its own settings by name. Only what is given changes. Sonarr sends a test through it before saving unless force is set.",
		deleteSays: "Delete a notification from Sonarr, with its settings and credentials. Nothing changes without confirm: the answer says what a confirmed call would delete.",
		testSays:   "Send a test through notifications the way Sonarr's Test button does, one by name or every one that is sent for something, and say what is wrong with any that fail. It sends a real message to where the notification points; it changes nothing in Sonarr.",
	})

	registerRecords(r, recordKind[sonarr.ImportListResource]{
		prefix: "importlist", one: "an import list", many: "import lists", nameKey: "name",
		loses: "its settings and its API key or login; the series it added stay in the library",
		list: func(ctx context.Context) ([]sonarr.ImportListResource, error) {
			res, err := c.GetImportList(ctx)
			return res.Model, err
		},
		kinds: func(ctx context.Context) ([]sonarr.ImportListResource, error) {
			res, err := c.GetImportListSchema(ctx)
			return res.Model, err
		},
		create: func(ctx context.Context, rec sonarr.ImportListResource, force bool) (*sonarr.ImportListResource, error) {
			res, err := c.PostImportList(ctx, rec, sonarr.PostImportListOperationOptions{ForceSave: new(force)})
			return res.Model, err
		},
		update: func(ctx context.Context, id int, rec sonarr.ImportListResource, force bool) (*sonarr.ImportListResource, error) {
			res, err := c.PutImportListById(ctx, id, rec, sonarr.PutImportListByIdOperationOptions{ForceSave: new(force)})
			return res.Model, err
		},
		remove: func(ctx context.Context, id int) error {
			_, err := c.DeleteImportListById(ctx, id)
			return err
		},
		test: func(ctx context.Context, rec sonarr.ImportListResource) error {
			_, err := c.PostImportListTest(ctx, rec, sonarr.PostImportListTestOperationOptions{ForceTest: new(true)})
			return err
		},
		testAll: func(ctx context.Context) ([]sonarr.ProviderTestAllResult, error) {
			res, err := c.PostImportListTestAll(ctx)
			return res.Model, err
		},
		on:       switchedOn("enableAutomaticAdd"),
		listSays: "The import lists: what adds series to Sonarr on its own - a Trakt or IMDb list, Plex watchlist, another Sonarr, Simkl, AniList - each with whether it adds automatically, the root folder, quality profile and monitoring its series get, its tags and its settings without credentials. exclusion_list is what they are told never to add.",
		// as Sonarr's own page starts one: adding what it finds, monitored,
		// in season folders
		defaults: func(blank map[string]any) {
			blank["enableAutomaticAdd"], blank["searchForMissingEpisodes"], blank["seasonFolder"] = true, true, true
			blank["shouldMonitor"], blank["monitorNewItems"], blank["seriesType"] = string(sonarr.MonitorTypesAll), string(sonarr.NewItemMonitorTypesAll), string(sonarr.SeriesTypesStandard)
		},
		kindsSays:  "The kinds of import list Sonarr can add - Trakt lists, IMDb, Plex watchlist, another Sonarr, Simkl, AniList, MyAnimeList, a custom list - each with the settings it takes. importlist_add takes one as kind.",
		addSays:    "Add an import list: its kind (importlist_kinds lists them), a name, its own settings by name, and with set how its series are added: rootFolderPath and qualityProfileId (by profile name or id), which it needs, and enableAutomaticAdd, searchForMissingEpisodes and seasonFolder (each on unless set false), shouldMonitor and monitorNewItems (all unless set), seriesType (standard unless set) and tags. Sonarr tries it first and refuses one that fails, unless force is set.",
		editSays:   "Change an import list: with set its name and how its series are added (enableAutomaticAdd, searchForMissingEpisodes, shouldMonitor, monitorNewItems, rootFolderPath, qualityProfileId by profile name or id, seriesType, seasonFolder, tags), and its own settings by name. Only what is given changes. Sonarr tries it before saving unless force is set.",
		deleteSays: "Delete an import list from Sonarr, with its settings and credentials; the series it added stay. Nothing changes without confirm: the answer says what a confirmed call would delete.",
		testSays:   "Test import lists the way Sonarr's Test button does - reach the list, authenticate, read it - one by name, or every one that adds automatically, and say what is wrong with any that fail. Changes nothing.",
	})

	// the metadata consumers are a fixed few, each written beside the files
	// or not: they are turned on and set, never added or deleted
	registerRecords(r, recordKind[sonarr.MetadataResource]{
		prefix: "metadata", one: "a metadata consumer", many: "metadata consumers", nameKey: "name",
		list: func(ctx context.Context) ([]sonarr.MetadataResource, error) {
			res, err := c.GetMetadata(ctx)
			return res.Model, err
		},
		update: func(ctx context.Context, id int, rec sonarr.MetadataResource, force bool) (*sonarr.MetadataResource, error) {
			res, err := c.PutMetadataById(ctx, id, rec, sonarr.PutMetadataByIdOperationOptions{ForceSave: new(force)})
			return res.Model, err
		},
		listSays: "The metadata Sonarr writes beside episode files for a media player to read - Kodi (XBMC) and Emby .nfo files and images, Roksbox, WDTV, Plex - each with whether it is on and what it writes.",
		editSays: "Turn a metadata consumer on or off (set enable) and choose what it writes beside the files (its own settings by name: seriesMetadata, episodeMetadata, seriesImages, seasonImages, episodeImages). Sonarr writes the files as it next refreshes each series.",
	})

	registerRecords(r, recordKind[sonarr.DelayProfileResource]{
		prefix: "delayprofile", one: "a delay profile", many: "delay profiles",
		loses: "its delays and the tags it applied to; series with those tags fall back to the default profile",
		blank: func() sonarr.DelayProfileResource {
			return sonarr.DelayProfileResource{EnableUsenet: new(true), EnableTorrent: new(true), PreferredProtocol: sonarr.DownloadProtocolUsenet, BypassIfHighestQuality: new(false), BypassIfAboveCustomFormatScore: new(false)}
		},
		list: func(ctx context.Context) ([]sonarr.DelayProfileResource, error) {
			res, err := c.GetDelayProfile(ctx)
			return res.Model, err
		},
		create: func(ctx context.Context, rec sonarr.DelayProfileResource, _ bool) (*sonarr.DelayProfileResource, error) {
			res, err := c.PostDelayProfile(ctx, rec)
			return res.Model, err
		},
		update: func(ctx context.Context, id int, rec sonarr.DelayProfileResource, _ bool) (*sonarr.DelayProfileResource, error) {
			res, err := c.PutDelayProfileById(ctx, strconv.Itoa(id), rec)
			return res.Model, err
		},
		remove: func(ctx context.Context, id int) error {
			_, err := c.DeleteDelayProfileById(ctx, id)
			return err
		},
		listSays:   "The delay profiles: how long Sonarr waits before grabbing a release, so a better one has time to appear - per protocol (usenetDelay, torrentDelay, in minutes), which protocol is preferred, and the tags of the series each applies to. The one with no tags is the default.",
		addSays:    "Add a delay profile for the series with certain tags: with set, usenetDelay and torrentDelay in minutes, preferredProtocol (usenet or torrent), enableUsenet, enableTorrent, bypassIfHighestQuality, and tags by label (a profile other than the default needs at least one).",
		editSays:   "Change a delay profile, by its id: with set, usenetDelay and torrentDelay in minutes, preferredProtocol, enableUsenet, enableTorrent, bypassIfHighestQuality, bypassIfAboveCustomFormatScore, minimumCustomFormatScore and tags.",
		deleteSays: "Delete a delay profile, by its id; the default one cannot be deleted. Nothing changes without confirm: the answer says what a confirmed call would delete.",
	})

	registerRecords(r, recordKind[sonarr.ReleaseProfileResource]{
		prefix: "releaseprofile", one: "a release profile", many: "release profiles", nameKey: "name",
		loses: "the words it required and ignored; releases it kept out are grabbed again",
		blank: func() sonarr.ReleaseProfileResource { return sonarr.ReleaseProfileResource{Enabled: new(true)} },
		list: func(ctx context.Context) ([]sonarr.ReleaseProfileResource, error) {
			res, err := c.GetReleaseProfile(ctx)
			return res.Model, err
		},
		create: func(ctx context.Context, rec sonarr.ReleaseProfileResource, _ bool) (*sonarr.ReleaseProfileResource, error) {
			res, err := c.PostReleaseProfile(ctx, rec)
			return res.Model, err
		},
		update: func(ctx context.Context, id int, rec sonarr.ReleaseProfileResource, _ bool) (*sonarr.ReleaseProfileResource, error) {
			res, err := c.PutReleaseProfileById(ctx, strconv.Itoa(id), rec)
			return res.Model, err
		},
		remove: func(ctx context.Context, id int) error {
			_, err := c.DeleteReleaseProfileById(ctx, id)
			return err
		},
		listSays:   "The release profiles: words a release's name must have (required) or must not have (ignored) to be grabbed, for every series or the ones with its tags, and for one indexer or all.",
		addSays:    "Add a release profile: a name, and with set the words a release must have (required) and must not have (ignored), each a list of terms or /regular expressions/, whether it is enabled, the indexer it applies to (indexerId, 0 for all), and tags by label (none applies it to every series).",
		editSays:   "Change a release profile: with set its name, required and ignored terms, enabled, indexerId and tags.",
		deleteSays: "Delete a release profile. Nothing changes without confirm: the answer says what a confirmed call would delete.",
	})

	registerRecords(r, recordKind[sonarr.RemotePathMappingResource]{
		prefix: "remotepathmapping", one: "a remote path mapping", many: "remote path mappings",
		loses: "the mapping; Sonarr then looks for that client's downloads at the path the client reports",
		blank: func() sonarr.RemotePathMappingResource { return sonarr.RemotePathMappingResource{} },
		list: func(ctx context.Context) ([]sonarr.RemotePathMappingResource, error) {
			res, err := c.GetRemotePathMapping(ctx)
			return res.Model, err
		},
		create: func(ctx context.Context, rec sonarr.RemotePathMappingResource, _ bool) (*sonarr.RemotePathMappingResource, error) {
			res, err := c.PostRemotePathMapping(ctx, rec)
			return res.Model, err
		},
		update: func(ctx context.Context, id int, rec sonarr.RemotePathMappingResource, _ bool) (*sonarr.RemotePathMappingResource, error) {
			res, err := c.PutRemotePathMappingById(ctx, strconv.Itoa(id), rec)
			return res.Model, err
		},
		remove: func(ctx context.Context, id int) error {
			_, err := c.DeleteRemotePathMappingById(ctx, id)
			return err
		},
		listSays:   "The remote path mappings: where a download client on another machine (host) puts its downloads as it sees them (remotePath), and where Sonarr finds the same folder (localPath). A stuck import with \"path does not exist\" is usually one of these missing.",
		addSays:    "Add a remote path mapping: with set, the download client's host as Sonarr's settings name it, the folder as the client reports it (remotePath) and the same folder as Sonarr sees it (localPath).",
		editSays:   "Change a remote path mapping, by its id: with set, host, remotePath and localPath.",
		deleteSays: "Delete a remote path mapping, by its id. Nothing changes without confirm: the answer says what a confirmed call would delete.",
	})

	// custom formats are listed by a tool of their own (config.go), with the
	// profiles that score them
	registerRecords(r, recordKind[sonarr.CustomFormatResource]{
		prefix: "customformat", one: "a custom format", many: "custom formats", nameKey: "name",
		loses: "its conditions, and its score in every quality profile",
		blank: func() sonarr.CustomFormatResource {
			return sonarr.CustomFormatResource{IncludeCustomFormatWhenRenaming: new(false)}
		},
		specs: func(ctx context.Context) ([]map[string]any, error) {
			res, err := c.GetCustomFormatSchema(ctx)
			if err != nil {
				return nil, err
			}
			return specMaps(res.Model)
		},
		list: func(ctx context.Context) ([]sonarr.CustomFormatResource, error) {
			res, err := c.GetCustomFormat(ctx)
			return res.Model, err
		},
		create: func(ctx context.Context, rec sonarr.CustomFormatResource, _ bool) (*sonarr.CustomFormatResource, error) {
			res, err := c.PostCustomFormat(ctx, rec)
			return res.Model, err
		},
		update: func(ctx context.Context, id int, rec sonarr.CustomFormatResource, _ bool) (*sonarr.CustomFormatResource, error) {
			res, err := c.PutCustomFormatById(ctx, strconv.Itoa(id), rec)
			return res.Model, err
		},
		remove: func(ctx context.Context, id int) error {
			_, err := c.DeleteCustomFormatById(ctx, id)
			return err
		},
		addSays:    "Add a custom format: a name, and with set its specifications, the conditions a release has to meet - each its implementation (ReleaseTitleSpecification, ReleaseGroupSpecification, SourceSpecification, ResolutionSpecification, LanguageSpecification, SizeSpecification, IndexerFlagSpecification, ReleaseTypeSpecification), a name, negate, required and its settings (value: a regular expression for a title or group, a number for a source or resolution) - and includeCustomFormatWhenRenaming. A format scores nothing until profile_edit gives it a score.",
		editSays:   "Change a custom format: with set its name, includeCustomFormatWhenRenaming, or its specifications, which replace the ones it has (customformat_list shows them).",
		deleteSays: "Delete a custom format; every quality profile loses its score for it. Nothing changes without confirm: the answer says what a confirmed call would delete.",
	})

	registerRecords(r, recordKind[sonarr.AutoTaggingResource]{
		prefix: "autotag", one: "an auto tag", many: "auto tags", nameKey: "name",
		loses: "its conditions; the tags it put on series stay unless it removed them itself",
		blank: func() sonarr.AutoTaggingResource {
			return sonarr.AutoTaggingResource{RemoveTagsAutomatically: new(false)}
		},
		specs: func(ctx context.Context) ([]map[string]any, error) {
			res, err := c.GetAutoTaggingSchema(ctx)
			if err != nil {
				return nil, err
			}
			return specMaps(res.Model)
		},
		list: func(ctx context.Context) ([]sonarr.AutoTaggingResource, error) {
			res, err := c.GetAutoTagging(ctx)
			return res.Model, err
		},
		create: func(ctx context.Context, rec sonarr.AutoTaggingResource, _ bool) (*sonarr.AutoTaggingResource, error) {
			res, err := c.PostAutoTagging(ctx, rec)
			return res.Model, err
		},
		update: func(ctx context.Context, id int, rec sonarr.AutoTaggingResource, _ bool) (*sonarr.AutoTaggingResource, error) {
			res, err := c.PutAutoTaggingById(ctx, strconv.Itoa(id), rec)
			return res.Model, err
		},
		remove: func(ctx context.Context, id int) error {
			_, err := c.DeleteAutoTaggingById(ctx, id)
			return err
		},
		listSays:   "The auto tags: rules that put tags on the series that meet their conditions - a genre, a network, a root folder, a series type, a status, a year, an original language, a quality profile - each with its conditions and the tags it applies.",
		addSays:    "Add an auto tag: a name, and with set the tags it applies (by label) and its specifications, the conditions a series has to meet - each its implementation (GenreSpecification, NetworkSpecification, RootFolderSpecification, SeriesTypeSpecification, StatusSpecification, YearSpecification, OriginalLanguageSpecification, QualityProfileSpecification, MonitoredSpecification), a name, negate, required and its settings (value) - and removeTagsAutomatically, to take the tags off a series that no longer meets them.",
		editSays:   "Change an auto tag: with set its name, tags, removeTagsAutomatically, or its specifications, which replace the ones it has.",
		deleteSays: "Delete an auto tag. Nothing changes without confirm: the answer says what a confirmed call would delete.",
	})

	registerRecords(r, recordKind[sonarr.CustomFilterResource]{
		prefix: "filter", one: "a custom filter", many: "custom filters", nameKey: "label",
		loses: "the saved filter in Sonarr's web interface; nothing in the library changes",
		blank: func() sonarr.CustomFilterResource { return sonarr.CustomFilterResource{} },
		list: func(ctx context.Context) ([]sonarr.CustomFilterResource, error) {
			res, err := c.GetCustomFilter(ctx)
			return res.Model, err
		},
		create: func(ctx context.Context, rec sonarr.CustomFilterResource, _ bool) (*sonarr.CustomFilterResource, error) {
			res, err := c.PostCustomFilter(ctx, rec)
			return res.Model, err
		},
		update: func(ctx context.Context, id int, rec sonarr.CustomFilterResource, _ bool) (*sonarr.CustomFilterResource, error) {
			res, err := c.PutCustomFilterById(ctx, strconv.Itoa(id), rec)
			return res.Model, err
		},
		remove: func(ctx context.Context, id int) error {
			_, err := c.DeleteCustomFilterById(ctx, id)
			return err
		},
		listSays:   "The custom filters saved in Sonarr's web interface: each one's label, the list it filters (type: series, calendar, queue, history, blocklist, wanted) and its conditions. They change what a person sees there, not what these tools answer.",
		addSays:    "Save a custom filter for Sonarr's web interface: a name (its label), and with set the list it filters (type: series, calendar, queue, history, blocklist, wanted) and its filters, each a key, a value and a type (equal, notEqual, contains, greaterThan, lessThan).",
		editSays:   "Change a saved custom filter: with set its label, type or filters.",
		deleteSays: "Delete a saved custom filter. Nothing changes without confirm: the answer says what a confirmed call would delete.",
	})
}
