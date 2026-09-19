// Package cassettes cuts the lists of every show a provider knows down to the
// shows a test suite uses, as the provider proxy records them.
//
// Sonarr fetches two such lists as it starts: the scene names from
// services.sonarr.tv (1.6 MB, every alias of every series) and XEM's names
// and mapped series. A suite reads only its own series' entries, so its
// recordings keep only those: the rest would be most of the cassettes, and
// churn on every re-record for shows no test touches.
package cassettes

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
)

// ForSeries returns the proxy's Trim option (providerproxy.Options.Trim) for
// a suite whose series have these TheTVDB ids.
func ForSeries(tvdbIDs ...int) map[string]func([]byte) ([]byte, error) {
	keep := func(id string) bool {
		n, err := strconv.Atoi(id)
		return err == nil && slices.Contains(tvdbIDs, n)
	}

	return map[string]func([]byte) ([]byte, error){
		"services.sonarr.tv/v1/scenemapping": func(body []byte) ([]byte, error) { return sceneMappings(body, keep) },
		"thexem.info/map/allNames":           func(body []byte) ([]byte, error) { return xemData(body, keep) },
		"thexem.info/map/havemap":            func(body []byte) ([]byte, error) { return xemData(body, keep) },
	}
}

// sceneMappings keeps the scene names of the kept series: a list of
// {"tvdbId": 78874, "title": ...}.
func sceneMappings(body []byte, keep func(string) bool) ([]byte, error) {
	var all []json.RawMessage
	if err := json.Unmarshal(body, &all); err != nil {
		return nil, err
	}
	kept := []json.RawMessage{}
	for _, m := range all {
		var id struct {
			TvdbID int `json:"tvdbId"`
		}
		if err := json.Unmarshal(m, &id); err != nil {
			return nil, err
		}
		if keep(strconv.Itoa(id.TvdbID)) {
			kept = append(kept, m)
		}
	}

	return json.Marshal(kept)
}

// xemData keeps the kept series in XEM's "data", which is keyed by TheTVDB id
// for the names, and a list of ids for the series XEM maps.
func xemData(body []byte, keep func(string) bool) ([]byte, error) {
	var answer map[string]json.RawMessage
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, err
	}
	data := answer["data"]
	var kept any
	switch {
	case len(data) > 0 && data[0] == '{':
		var byID map[string]json.RawMessage
		if err := json.Unmarshal(data, &byID); err != nil {
			return nil, err
		}
		for id := range byID {
			if !keep(id) {
				delete(byID, id)
			}
		}
		kept = byID
	case len(data) > 0 && data[0] == '[':
		var ids []json.RawMessage
		if err := json.Unmarshal(data, &ids); err != nil {
			return nil, err
		}
		list := []json.RawMessage{}
		for _, id := range ids {
			if keep(strings.Trim(string(id), `"`)) {
				list = append(list, id)
			}
		}
		kept = list
	default:
		return nil, errors.New("an XEM answer with no data")
	}
	b, err := json.Marshal(kept)
	if err != nil {
		return nil, err
	}
	answer["data"] = b

	return json.Marshal(answer)
}
