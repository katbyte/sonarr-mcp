package cassettes

import (
	"testing"
)

func trim(t *testing.T, key, body string) string {
	t.Helper()

	fn := ForSeries(78874, 371980)[key]
	if fn == nil {
		t.Fatalf("no trim for %s", key)
	}
	out, err := fn([]byte(body))
	if err != nil {
		t.Fatal(err)
	}

	return string(out)
}

// Every alias of every show, cut to the suite's: each kept entry exactly as
// it came.
func TestSceneMappings(t *testing.T) {
	t.Parallel()

	got := trim(t, "services.sonarr.tv/v1/scenemapping", `[
		{"mappingId":"a","tvdbId":152831,"title":"Adventure Time With Finn and Jake","season":-1},
		{"mappingId":"b","tvdbId":371980,"title":"Scissione","season":-1},
		{"mappingId":"c","tvdbId":78874,"title":"Firefly - Der Aufbruch der Serenity","season":-1}
	]`)
	want := `[{"mappingId":"b","tvdbId":371980,"title":"Scissione","season":-1},{"mappingId":"c","tvdbId":78874,"title":"Firefly - Der Aufbruch der Serenity","season":-1}]`
	if got != want {
		t.Errorf("= %s\nwant %s", got, want)
	}
	if got := trim(t, "services.sonarr.tv/v1/scenemapping", `[{"tvdbId":1}]`); got != `[]` {
		t.Errorf("none kept = %s, want an empty list rather than null", got)
	}
}

// XEM's names are keyed by id, its mapped series a list of ids; the rest of
// the answer is kept as it is.
func TestXEM(t *testing.T) {
	t.Parallel()

	names := trim(t, "thexem.info/map/allNames", `{"result":"success","data":{"257875":[{"Lupin III":-1}],"78874":[{"Firefly":1}]},"message":""}`)
	if want := `{"data":{"78874":[{"Firefly":1}]},"message":"","result":"success"}`; names != want {
		t.Errorf("allNames = %s\nwant %s", names, want)
	}
	mapped := trim(t, "thexem.info/map/havemap", `{"result":"success","data":["70668","371980",78874]}`)
	if want := `{"data":["371980",78874],"result":"success"}`; mapped != want {
		t.Errorf("havemap = %s\nwant %s", mapped, want)
	}
}

// An answer that is not what the trim expects fails the recording rather
// than being stored cut to nothing.
func TestUnexpectedAnswers(t *testing.T) {
	t.Parallel()

	for key, body := range map[string]string{
		"services.sonarr.tv/v1/scenemapping": `{"error":"rate limited"}`,
		"thexem.info/map/allNames":           `{"result":"failure","message":"down"}`,
		"thexem.info/map/havemap":            `not json`,
	} {
		if out, err := ForSeries(78874)[key]([]byte(body)); err == nil {
			t.Errorf("%s of %s = %s, want an error", key, body, out)
		}
	}
}
