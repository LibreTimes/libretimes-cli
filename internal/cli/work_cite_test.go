package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
)

const citeWorkID = "0190bbbb-0000-7000-8000-000000000001"

func TestCiteWritesTheServersCSLAsAnArray(t *testing.T) {
	var gotQuery string
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		gotQuery = r.URL.RawQuery
		if r.URL.Path != "/works/"+citeWorkID+"/citation" {
			return false
		}
		w.Write([]byte(`{"work_id":"` + citeWorkID + `","expression_id":"` + exprA + `","volume":2,` +
			`"csl":{"type":"book","title":"Set","volume":"2","volume-title":"Языки","number-of-volumes":"3"}}`))
		return true
	})
	vol := 2
	data, err := citeWork(context.Background(), api.New(rec.server.URL, "", "test"), citeWorkID,
		api.CitationQuery{Expression: exprA, Volume: &vol, Lang: "ru"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "expression="+exprA) || !strings.Contains(gotQuery, "volume=2") ||
		!strings.Contains(gotQuery, "lang=ru") {
		t.Errorf("query = %q", gotQuery)
	}
	var items []map[string]any
	if err := json.Unmarshal(data, &items); err != nil || len(items) != 1 {
		t.Fatalf("not a one-item CSL array: %v %s", err, data)
	}
	if items[0]["volume-title"] != "Языки" || items[0]["number-of-volumes"] != "3" {
		t.Errorf("csl = %v", items[0])
	}
	if strings.Contains(rec.calls(), "POST") {
		t.Errorf("a citation is a read: %s", rec.calls())
	}
}

func TestCiteOfAMissingEditionIsTheServersRefusal(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"detail":"That edition is not this work's"}`)
		return true
	})
	_, err := citeWork(context.Background(), api.New(rec.server.URL, "", "test"), citeWorkID,
		api.CitationQuery{Expression: exprB})
	var apiErr *api.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "not_found" {
		t.Fatalf("err = %v", err)
	}
}

func TestRecordSendsTheFileAsIsAndReportsAnExistingRecord(t *testing.T) {
	created := true
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/works/records" {
			return false
		}
		if created {
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"work":{"id":"`+citeWorkID+`","title":"Введение в теорию матриц","type":"book"},"created":true}`)
		} else {
			io.WriteString(w, `{"work":{"id":"`+citeWorkID+`","title":"Введение в теорию матриц","type":"book"},"created":false,"matched_by":"isbn"}`)
		}
		return true
	})
	client := api.New(rec.server.URL, "tok", "test")
	file := writeFile(t, t.TempDir(), "r.yaml", `type: book
title: Введение в теорию матриц
language: ru
names:
  - {display_name: Р. Беллман, role: author}
editions:
  - {language: ru, publisher: Наука, year: 1969, isbn: "9785020137004"}
`)
	body, err := readRecordFile(file, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := recordWork(context.Background(), client, body, false)
	if err != nil || first.Action != "create" || first.WorkID != citeWorkID {
		t.Fatalf("first = %+v, %v", first, err)
	}
	sent := rec.bodies[0]
	ed := sent["editions"].([]any)[0].(map[string]any)
	if sent["type"] != "book" || ed["isbn"] != "9785020137004" || ed["year"] != float64(1969) {
		t.Errorf("body = %v", sent)
	}
	created = false
	again, err := recordWork(context.Background(), client, body, false)
	if err != nil || again.Action != "exists" || again.MatchedBy != "isbn" {
		t.Fatalf("again = %+v, %v", again, err)
	}
}

func TestRecordDryRunSendsNothingAndAFileMissingARequiredKeyIsRefused(t *testing.T) {
	rec := newRecorder(t, nil)
	body := map[string]any{"type": "book", "title": "T", "language": "en"}
	r, err := recordWork(context.Background(), api.New(rec.server.URL, "tok", "test"), body, true)
	if err != nil || r.Action != "dry_run" || rec.calls() != "" {
		t.Fatalf("dry run = %+v, %v, calls %q", r, err, rec.calls())
	}
	file := writeFile(t, t.TempDir(), "bad.yaml", "title: T\n")
	_, err = readRecordFile(file, nil)
	if err == nil || !strings.Contains(err.Error(), "`type` is required") ||
		!strings.Contains(err.Error(), "`language` is required") {
		t.Fatalf("err = %v", err)
	}
}
