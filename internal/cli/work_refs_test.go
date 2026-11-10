package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
)

func TestCiteInAStylePostsTheServersCSLToTheFormatter(t *testing.T) {
	var posted map[string]any
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/works/" + citeWorkID + "/citation":
			w.Write([]byte(`{"work_id":"` + citeWorkID + `","csl":{"id":"x","type":"book","title":"Set"}}`))
		case "/citations/format":
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &posted)
			w.Write([]byte(`{"style":"apa","lang":"ru","entries":[{"id":"x","runs":[],"text":"Set. (б. д.)."}]}`))
		default:
			return false
		}
		return true
	})
	data, err := citeWorkStyled(context.Background(), api.New(rec.server.URL, "", "test"), citeWorkID,
		api.CitationQuery{Lang: "ru"}, "apa")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "Set. (б. д.).\n" {
		t.Errorf("out = %q", data)
	}
	items, _ := posted["items"].([]any)
	if posted["style"] != "apa" || posted["lang"] != "ru" || len(items) != 1 {
		t.Errorf("posted = %v", posted)
	}
}

func TestRefsListReadsTheWorksBibliography(t *testing.T) {
	var gotQuery string
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/works/"+citeWorkID+"/bibliography" {
			return false
		}
		gotQuery = r.URL.RawQuery
		w.Write([]byte(`{"style":"ieee","lang":"en","entries":[{"id":"Hardy1940","runs":[],"text":"G. H. Hardy, A mathematician's apology."}]}`))
		return true
	})
	bib, err := api.New(rec.server.URL, "", "test").GetBibliography(context.Background(), citeWorkID,
		api.BibliographyQuery{Version: 2, Style: "ieee"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "version=2") || !strings.Contains(gotQuery, "style=ieee") || strings.Contains(gotQuery, "lang") {
		t.Errorf("query = %q", gotQuery)
	}
	if len(bib.Entries) != 1 || bib.Entries[0].ID != "Hardy1940" {
		t.Errorf("bib = %+v", bib)
	}
}
