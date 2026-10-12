package tools

// Most of what a tool answers was written by someone else: a release's name as a public indexer gave it, a title and a plot from TheTVDB, a message
// from a download client, a line of Sonarr's log quoting any of them. A model reads it as it finds it, so every piece of text in an answer, and in a
// refusal, has what does not show taken out of it and is cut to a length, once, around every tool, by go-kt's registry (queued, in all_tools.go).
// What is here is what the registry is told: how long, what to leave as it is, and where Sonarr quotes an address.

// answerLength is the most of one piece of text an answer carries.
const answerLength = 2000

// handles are the values in an answer that a later call hands back, to a tool here or through it to Sonarr, by the names they are answered under: a
// path or a folder, a release's guid, a download's id, and an audit's subject, which is a path as often as not. Each has to find the same thing
// again, so it is answered as Sonarr holds it and not cleaned. A series' title is handed back too, and is cleaned: titles are compared with what
// does not show taken out (normaliseTitle).
//
// The last three are Sonarr's own names, and no tool's field: a settings record is answered as a map by the names Sonarr gives its properties, and
// these are the paths among them, the folder an import list adds series under and the two sides of a remote path mapping.
var handles = []string{
	"path", "relative_path", "output_path", "from", "to",
	"folder", "unmapped_folders", "root_folder",
	"guid", "download_id", "subject",
	"rootFolderPath", "remotePath", "localPath",
}

// sonarrSaid are the values in an answer that are Sonarr's own words for what went wrong, or a setting it holds, by the names they are answered
// under. Sonarr quotes an address back with a credential of its own on the end of it: an indexer that fails its test is said to have failed "at" the
// address Sonarr asked, the API key it keeps for that indexer and all, and a webhook's address is kept with its token on it. That key was never the
// caller's to be handed, so an address under one of these names is shown without who it signs in as, the value of any parameter, or what follows a
// #. An address anywhere else in an answer is a link to follow and is left; one in a refusal is blanked by the registry unasked.
var sonarrSaid = []string{
	"problems", "message", "exception", "messages", "detail", "error", "rejections", "lines",
	"settings", "changed", "would_change", "not_kept",
}
