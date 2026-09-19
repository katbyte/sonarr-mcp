package sabnzbd

// The answers, in the shapes SABnzbd 4 sends and Sonarr's SabnzbdProxy
// deserializes. Where SABnzbd sends a number as a string (mb, mbleft,
// percentage) or a flag as 0 or 1 (pre_check), so does this: Sonarr reads
// those leniently, and a mirror of it that expects the lenient shape is what
// the tests hold these to.

// statusAnswer is an action's answer, and with Error set a failure: Sonarr's
// CheckForError throws on status false with the error's words.
type statusAnswer struct {
	Status bool   `json:"status"`
	Error  string `json:"error,omitempty"`
}

func failure(message string) statusAnswer { return statusAnswer{Status: false, Error: message} }

type versionAnswer struct {
	Version string `json:"version"`
}

// addAnswer is addfile's, addurl's and a queue action's: the jobs it made or
// touched (SabnzbdAddResponse).
type addAnswer struct {
	Status bool     `json:"status"`
	NzoIDs []string `json:"nzo_ids"`
}

type retryAnswer struct {
	Status bool   `json:"status"`
	NzoID  string `json:"nzo_id"`
}

type categoriesAnswer struct {
	Categories []string `json:"categories"`
}

type fullStatusAnswer struct {
	Status fullStatus `json:"status"`
}

type fullStatus struct {
	CompleteDir string `json:"completedir"`
	DownloadDir string `json:"downloaddir"`
	Version     string `json:"version"`
	Paused      bool   `json:"paused"`
}

type configAnswer struct {
	Config config `json:"config"`
}

type config struct {
	Misc       configMisc       `json:"misc"`
	Categories []configCategory `json:"categories"`
	Servers    []configServer   `json:"servers"`
	Sorters    []configSorter   `json:"sorters"`
}

// configMisc is the part of [misc] Sonarr reads. The old per-kind sorting
// switches (enable_tv_sorting and the rest) went in SABnzbd 4.1, when sorting
// became the sorters list, so they are absent, as they are from a real 4.x.
type configMisc struct {
	CompleteDir            string `json:"complete_dir"`
	DownloadDir            string `json:"download_dir"`
	PreCheck               int    `json:"pre_check"`
	HistoryRetention       string `json:"history_retention"`
	HistoryRetentionOption string `json:"history_retention_option"`
	HistoryRetentionNumber int    `json:"history_retention_number"`
}

type configCategory struct {
	Name     string `json:"name"`
	Order    int    `json:"order"`
	PP       string `json:"pp"`
	Script   string `json:"script"`
	Dir      string `json:"dir"`
	Newzbin  string `json:"newzbin"`
	Priority int    `json:"priority"`
}

// configServer is a news server; the fake lists none, and nothing reads them.
type configServer struct {
	Name string `json:"name"`
}

// configSorter is a SABnzbd 4.1+ sorter; the fake lists none, so none is
// active on Sonarr's category.
type configSorter struct {
	Name     string   `json:"name"`
	IsActive bool     `json:"is_active"`
	SortCats []string `json:"sort_cats"`
}

type queueAnswer struct {
	Queue queue `json:"queue"`
}

type queue struct {
	Version        string      `json:"version"`
	Paused         bool        `json:"paused"`
	PausedAll      bool        `json:"paused_all"`
	Status         string      `json:"status"`
	SpeedLimit     string      `json:"speedlimit"`
	Speed          string      `json:"speed"`
	KBPerSec       string      `json:"kbpersec"`
	Size           string      `json:"size"`
	SizeLeft       string      `json:"sizeleft"`
	MB             string      `json:"mb"`
	MBLeft         string      `json:"mbleft"`
	NoOfSlotsTotal int         `json:"noofslots_total"`
	NoOfSlots      int         `json:"noofslots"`
	Start          int         `json:"start"`
	Limit          int         `json:"limit"`
	Finish         int         `json:"finish"`
	TimeLeft       string      `json:"timeleft"`
	Slots          []queueSlot `json:"slots"`
}

// queueSlot is one queued job (SabnzbdQueueItem).
type queueSlot struct {
	Index      int      `json:"index"`
	NzoID      string   `json:"nzo_id"`
	UnpackOpts string   `json:"unpackopts"`
	Priority   string   `json:"priority"`
	Script     string   `json:"script"`
	Filename   string   `json:"filename"`
	Labels     []string `json:"labels"`
	Password   string   `json:"password"`
	Cat        string   `json:"cat"`
	MBLeft     string   `json:"mbleft"`
	MB         string   `json:"mb"`
	Size       string   `json:"size"`
	SizeLeft   string   `json:"sizeleft"`
	Percentage string   `json:"percentage"`
	MBMissing  string   `json:"mbmissing"`
	Status     string   `json:"status"`
	TimeLeft   string   `json:"timeleft"`
	AvgAge     string   `json:"avg_age"`
}

type historyAnswer struct {
	History history `json:"history"`
}

type history struct {
	NoOfSlots         int           `json:"noofslots"`
	PPSlots           int           `json:"ppslots"`
	DaySize           string        `json:"day_size"`
	WeekSize          string        `json:"week_size"`
	MonthSize         string        `json:"month_size"`
	TotalSize         string        `json:"total_size"`
	LastHistoryUpdate int64         `json:"last_history_update"`
	Slots             []historySlot `json:"slots"`
}

// historySlot is one finished job (SabnzbdHistoryItem).
type historySlot struct {
	Completed    int64    `json:"completed"`
	Name         string   `json:"name"`
	NzbName      string   `json:"nzb_name"`
	Category     string   `json:"category"`
	PP           string   `json:"pp"`
	Script       string   `json:"script"`
	URL          string   `json:"url"`
	Status       string   `json:"status"`
	NzoID        string   `json:"nzo_id"`
	Storage      string   `json:"storage"`
	Path         string   `json:"path"`
	DownloadTime int      `json:"download_time"`
	PostprocTime int      `json:"postproc_time"`
	StageLog     []string `json:"stage_log"`
	Downloaded   int64    `json:"downloaded"`
	FailMessage  string   `json:"fail_message"`
	Bytes        int64    `json:"bytes"`
	Size         string   `json:"size"`
	Retry        int      `json:"retry"`
	Archive      bool     `json:"archive"`
	TimeAdded    int64    `json:"time_added"`
}
