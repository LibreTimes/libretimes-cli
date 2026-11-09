package cli

import (
	"slices"
	"strings"
	"testing"
)

const (
	exprA = "aaaaaaaa-0000-4000-8000-000000000001"
	exprB = "bbbbbbbb-0000-4000-8000-000000000002"
	work1 = "11111111-1111-4111-8111-111111111111"
	work2 = "22222222-2222-4222-8222-222222222222"
	work3 = "33333333-3333-4333-8333-333333333333"
)

func readingList(items string) string {
	return "---\nimport_key: sh/rl\ntitle: Reading\nlanguage: en\nkind: reading_list\nkeywords: [topology, algebra]\npublic_comments: false\n" +
		items + "---\n"
}

const twoSectionList = `items:
  - ` + work1 + `
sections:
  - title: Basics
    description: Start here
    items:
      - work: https://libretimes.io/ru/works/` + work2 + `
        note: Read chapters 1-3
        edition: ` + exprA + `
  - title: Advanced
    items:
      - ` + work3 + "\n"

func sectionsAPI() *collectionAPI {
	return &collectionAPI{createKey: "sh/rl", works: map[string]string{
		work1: `{"id":"` + work1 + `","title":"One"}`,
		work2: `{"id":"` + work2 + `","title":"Two"}`,
		work3: `{"id":"` + work3 + `","title":"Three"}`,
	}}
}

func TestReadingListCreatesSectionsNotesAndPins(t *testing.T) {
	f := sectionsAPI()
	path := writeFile(t, t.TempDir(), "README.md", readingList(twoSectionList))
	result, rec := runCollection(t, f, path, false)

	if result.Action != actionCreate || result.CollectionType != "reading_list" {
		t.Fatalf("result = %+v", result)
	}
	if f.created["collection_type"] != "reading_list" || f.created["public_comments"] != false {
		t.Errorf("create body = %v", f.created)
	}
	if kw, _ := f.created["keywords"].([]any); len(kw) != 2 {
		t.Errorf("keywords = %v", f.created["keywords"])
	}
	if len(f.sectionNew) != 2 || f.sectionNew[0]["title"] != "Basics" ||
		f.sectionNew[0]["description"] != "Start here" || f.sectionNew[1]["title"] != "Advanced" {
		t.Errorf("sections = %v", f.sectionNew)
	}
	if len(f.addedItems) != 3 {
		t.Fatalf("added = %v", f.addedItems)
	}
	if _, has := f.addedItems[0]["section_id"]; has {
		t.Errorf("the preamble item has no section: %v", f.addedItems[0])
	}
	two := f.addedItems[1]
	if two["entity_id"] != work2 || two["section_id"] != "s-new-1" ||
		two["note"] != "Read chapters 1-3" || two["expression_id"] != exprA {
		t.Errorf("second item = %v", two)
	}
	if f.addedItems[2]["section_id"] != "s-new-2" {
		t.Errorf("third item = %v", f.addedItems[2])
	}
	if strings.Contains(rec.calls(), "PUT") {
		t.Errorf("a fresh list is appended in order: %s", rec.calls())
	}
}

func TestReadingListRerunChangesNothing(t *testing.T) {
	f := sectionsAPI()
	f.coll = `{"id":"coll-1","import_key":"sh/rl"}`
	f.sections = []map[string]any{
		{"id": "s1", "title": "Basics", "position": 0, "description": "Start here"},
		{"id": "s2", "title": "Advanced", "position": 1},
	}
	f.items = []string{work1, work2, work3}
	f.itemExtra = map[string]map[string]any{
		work2: {"section_id": "s1", "note": "Read chapters 1-3", "expression_id": exprA},
		work3: {"section_id": "s2"},
	}
	path := writeFile(t, t.TempDir(), "README.md", readingList(twoSectionList))
	result, rec := runCollection(t, f, path, false)

	if result.Action != actionUpdate || result.Reordered {
		t.Fatalf("result = %+v", result)
	}
	if strings.Contains(rec.calls(), "PUT") || strings.Contains(rec.calls(), "/sections") ||
		strings.Contains(rec.calls(), "/items") && !strings.Contains(rec.calls(), "GET") {
		t.Errorf("an unchanged list wrote: %s", rec.calls())
	}
	if len(f.addedItems) != 0 || len(f.itemPatches) != 0 || len(f.sectionNew) != 0 || len(f.sectionPatch) != 0 {
		t.Errorf("writes: %v %v %v %v", f.addedItems, f.itemPatches, f.sectionNew, f.sectionPatch)
	}
	for _, r := range result.Items {
		if r.Action != itemPresent {
			t.Errorf("item %s = %s, want present", r.Path, r.Action)
		}
	}
}

func TestReadingListRerunAddsSectionSetsNoteAndPinAndMoves(t *testing.T) {
	f := sectionsAPI()
	f.coll = `{"id":"coll-1","import_key":"sh/rl"}`
	f.sections = []map[string]any{{"id": "s1", "title": "Basics", "position": 0}}
	// work2 sits in the preamble with no note or pin; work3 is not held yet.
	f.items = []string{work1, work2}
	path := writeFile(t, t.TempDir(), "README.md", readingList(twoSectionList))
	result, _ := runCollection(t, f, path, false)

	if result.Action != actionUpdate || !result.Reordered {
		t.Fatalf("result = %+v", result)
	}
	if len(f.sectionNew) != 1 || f.sectionNew[0]["title"] != "Advanced" {
		t.Errorf("new sections = %v", f.sectionNew)
	}
	if f.sectionPatch["s1"]["description"] != "Start here" {
		t.Errorf("section patch = %v", f.sectionPatch)
	}
	if len(f.addedItems) != 1 || f.addedItems[0]["entity_id"] != work3 || f.addedItems[0]["section_id"] != "s-new-1" {
		t.Errorf("added = %v", f.addedItems)
	}
	patch := f.itemPatches[work2]
	if patch["note"] != "Read chapters 1-3" || patch["expression_id"] != exprA {
		t.Errorf("item patch = %v", f.itemPatches)
	}
	groups, _ := f.orderBody["sections"].([]any)
	if len(groups) != 3 {
		t.Fatalf("order body = %v", f.orderBody)
	}
	want := []struct {
		section any
		ids     []string
	}{{nil, []string{work1}}, {"s1", []string{work2}}, {"s-new-1", []string{work3}}}
	for i, w := range want {
		g := groups[i].(map[string]any)
		var ids []string
		for _, id := range g["entity_ids"].([]any) {
			ids = append(ids, id.(string))
		}
		if g["section_id"] != w.section || !slices.Equal(ids, w.ids) {
			t.Errorf("group %d = %v, want %v %v", i, g, w.section, w.ids)
		}
	}
}

func TestReadingListDryRunWritesNothing(t *testing.T) {
	f := sectionsAPI()
	f.coll = `{"id":"coll-1"}`
	f.items = []string{work1}
	path := writeFile(t, t.TempDir(), "README.md", readingList(twoSectionList))
	result, rec := runCollection(t, f, path, true)
	if rec.writeSum != 0 || strings.Contains(rec.calls(), "PUT") {
		t.Errorf("--dry-run wrote: %s", rec.calls())
	}
	if len(result.Sections) != 2 || result.Sections[0].Action != actionCreate {
		t.Errorf("sections = %+v", result.Sections)
	}
}

func TestSectionTitlesMustBeUniqueAndPresent(t *testing.T) {
	for name, items := range map[string]string{
		"dup":   "sections:\n  - title: A\n  - title: A\n",
		"empty": "sections:\n  - description: x\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := writeFile(t, t.TempDir(), "README.md", readingList(items))
			result, rec := runCollection(t, sectionsAPI(), path, false)
			if result.Code != "validation_error" || len(rec.mu) != 0 {
				t.Errorf("result = %+v calls = %s", result, rec.calls())
			}
		})
	}
}

func TestItemForms(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "w.md", lecture("k"))
	cases := []struct {
		spec, kind, value string
		wantErr           string
	}{
		{"w.md", refFile, "", ""},
		{work1, refID, work1, ""},
		{"https://libretimes.io/works/" + strings.ToUpper(work1), refID, work1, ""},
		{"https://libretimes.io/ru/works/" + work1 + "?x=1", refID, work1, ""},
		{"10.1000/xyz123", refDOI, "", ""},
		{"doi:10.1000/xyz123", refDOI, "", ""},
		{"https://doi.org/10.1000/xyz123", refDOI, "", ""},
		{"arxiv:2101.00001", refArxiv, "", ""},
		{"2101.00001v2", refArxiv, "", ""},
		{"https://arxiv.org/abs/2101.00001", refArxiv, "", ""},
		{"978-0-306-40615-7", refISBN, "9780306406157", ""},
		{"ISBN 0-306-40615-2", refISBN, "9780306406157", ""},
		{"book:ia/foo", "", "", "no read"},
		{"missing.md", "", "", "missing.md"},
		{"978-0-306-40615-8", "", "", "not a work file"}, // bad check digit
	}
	for _, c := range cases {
		ref, err := classifyItem(dir, c.spec)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%q: err = %v, want %q", c.spec, err, c.wantErr)
			}
			continue
		}
		if err != nil || ref.kind != c.kind || (c.value != "" && ref.value != c.value) {
			t.Errorf("%q = %+v, %v; want %s %s", c.spec, ref, err, c.kind, c.value)
		}
	}
}

func listWith(item string) string {
	return readingList("items:\n" + item)
}

func TestIdentifierItemsUseHeldWorksOnly(t *testing.T) {
	f := sectionsAPI()
	f.resolve = map[string]string{
		"10.1000/held": `{"kind":"doi","indexed":{"id":"` + work1 + `","title":"One"}}`,
		// Resolved at Crossref but not held: lt must not mint it.
		"10.1000/new": `{"kind":"doi","resolved":{"title":"X"}}`,
	}
	path := writeFile(t, t.TempDir(), "README.md", listWith("  - 10.1000/held\n  - 10.1000/new\n"))
	result, rec := runCollection(t, f, path, false)
	if want := []string{"add:", "skip:not_indexed"}; !slices.Equal(actions(result), want) {
		t.Errorf("items = %v, want %v", actions(result), want)
	}
	if !slices.Equal(f.added, []string{work1}) {
		t.Errorf("added = %v", f.added)
	}
	if strings.Contains(rec.calls(), "POST /works ") || strings.Contains(rec.calls(), "resolve-doi") {
		t.Errorf("a work was minted or resolved remotely: %s", rec.calls())
	}
}

func TestISBNPinsTheMatchingEdition(t *testing.T) {
	f := sectionsAPI()
	f.resolve = map[string]string{"9780306406157": `{"kind":"isbn","suggestions":[` +
		`{"id":"` + work1 + `","title":"One"},{"id":"` + work2 + `","title":"Two"}]}`}
	f.expressions = map[string]string{
		work1: `[{"id":"` + exprA + `","isbn13":"9780306406157"}]`,
		work2: `[{"id":"` + exprB + `","isbn13":"9781234567897"}]`,
	}
	path := writeFile(t, t.TempDir(), "README.md", listWith("  - 0-306-40615-2\n"))
	result, _ := runCollection(t, f, path, false)
	if result.Action != actionCreate || len(f.addedItems) != 1 ||
		f.addedItems[0]["entity_id"] != work1 || f.addedItems[0]["expression_id"] != exprA {
		t.Fatalf("result = %+v added = %v", result, f.addedItems)
	}
}

func TestISBNWithSeveralMatchesIsRefusedWithCandidates(t *testing.T) {
	f := sectionsAPI()
	f.resolve = map[string]string{"9780306406157": `{"kind":"isbn","suggestions":[` +
		`{"id":"` + work1 + `","title":"One"},{"id":"` + work2 + `","title":"Two"}]}`}
	f.expressions = map[string]string{
		work1: `[{"id":"` + exprA + `","isbn13":"9780306406157"}]`,
		work2: `[{"id":"` + exprB + `","isbn13":"9780306406157"}]`,
	}
	path := writeFile(t, t.TempDir(), "README.md", listWith("  - 9780306406157\n"))
	result, rec := runCollection(t, f, path, false)
	if result.Action != actionFailed || !strings.Contains(result.Message, work1) ||
		!strings.Contains(result.Message, work2) {
		t.Fatalf("result = %+v", result)
	}
	if strings.Contains(rec.calls(), "POST /collections") {
		t.Errorf("a refused manifest wrote: %s", rec.calls())
	}
}

func TestBookKeyIsRejectedBeforeAnyRequest(t *testing.T) {
	path := writeFile(t, t.TempDir(), "README.md", listWith("  - book:standardebooks/x\n"))
	result, rec := runCollection(t, sectionsAPI(), path, false)
	if result.Action != actionFailed || !strings.Contains(result.Message, "book:") || len(rec.mu) != 0 {
		t.Fatalf("result = %+v calls = %s", result, rec.calls())
	}
}

func TestSameWorkListedTwiceIsRefused(t *testing.T) {
	path := writeFile(t, t.TempDir(), "README.md", listWith("  - "+work1+"\n  - https://libretimes.io/works/"+work1+"\n"))
	result, _ := runCollection(t, sectionsAPI(), path, false)
	if result.Action != actionFailed || !strings.Contains(result.Message, "same work") {
		t.Fatalf("result = %+v", result)
	}
}

func TestUnknownWorkIDIsSkipped(t *testing.T) {
	path := writeFile(t, t.TempDir(), "README.md", listWith("  - "+work1+"\n"))
	result, _ := runCollection(t, &collectionAPI{}, path, false)
	if want := []string{"skip:not_found"}; !slices.Equal(actions(result), want) {
		t.Errorf("items = %v", actions(result))
	}
}

func TestEditionMustBeAnID(t *testing.T) {
	path := writeFile(t, t.TempDir(), "README.md", listWith("  - work: "+work1+"\n    edition: second\n"))
	result, rec := runCollection(t, sectionsAPI(), path, false)
	if result.Action != actionFailed || len(rec.mu) != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestFillSlots(t *testing.T) {
	cases := []struct{ cur, want, out []string }{
		{[]string{"a", "x", "b"}, []string{"b", "a"}, []string{"b", "x", "a"}},
		{[]string{"x"}, []string{"a", "b"}, []string{"x", "a", "b"}},
		{[]string{"a", "b"}, []string{"a", "b"}, []string{"a", "b"}},
	}
	for _, c := range cases {
		if got := fillSlots(c.cur, c.want); !slices.Equal(got, c.out) {
			t.Errorf("fillSlots(%v, %v) = %v, want %v", c.cur, c.want, got, c.out)
		}
	}
}
