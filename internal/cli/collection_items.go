package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
	"github.com/The-LibreTimes/libretimes-cli/internal/frontmatter"
)

// A listed work is named in one of these ways. lt only ever FINDS a work the
// site already holds: no form here mints or persists one.
const (
	refFile  = "file"  // a work file, by the import_key in its frontmatter
	refID    = "id"    // a work id, or a /works/<id> URL
	refDOI   = "doi"   // a DOI, or a doi.org URL
	refArxiv = "arxiv" // an arXiv id or URL
	refISBN  = "isbn"  // an ISBN-10 or ISBN-13
	refKey   = "key"   // a library catalogue key, `book:<key>`
)

var (
	doiPattern      = regexp.MustCompile(`(?i)^(?:doi:|https?://(?:dx\.)?doi\.org/)?(10\.\d{4,9}/\S+)$`)
	arxivNewPattern = regexp.MustCompile(`(?i)^\d{4}\.\d{4,5}(?:v\d+)?$`)
	arxivPrefix     = regexp.MustCompile(`(?i)^(?:arxiv:|https?://(?:www\.)?arxiv\.org/(?:abs|pdf)/)(\S+?)(?:\.pdf)?$`)
	isbnPrefix      = regexp.MustCompile(`(?i)^isbn(?:-1[03])?:?\s*`)
	// catalogueKey is the shape every seam's import key has: `book:<key>`,
	// `mccme-free-books:<key>`. Tried after every other form.
	catalogueKey = regexp.MustCompile(`^[a-z][a-z0-9-]*:\S+$`)
	// langYear names an edition by its language and year: `ru-1976`.
	langYear = regexp.MustCompile(`^([a-z]{2,3}(?:-[A-Za-z0-9]{2,8})*)-(\d{4})$`)
)

type itemRef struct {
	kind  string
	value string // file path (absolute), work id, or the ISBN-13
}

// manifestEntry is one listed item and the section it sits under.
type manifestEntry struct {
	spec    frontmatter.CollectionItem
	section int // -1 is the preamble
}

// classifyItem decides what a listed string names, without any request. A file
// that exists wins, so the manifests written before the other forms existed
// read as they always did.
func classifyItem(dir, spec string) (itemRef, error) {
	if spec == "" {
		return itemRef{}, fmt.Errorf("an item has no `work`")
	}
	path := filepath.FromSlash(spec)
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	if isFile(path) {
		return itemRef{refFile, path}, nil
	}
	if id, ok := idFromArg(spec, "works"); ok {
		return itemRef{refID, id}, nil
	}
	if doiPattern.MatchString(spec) {
		return itemRef{refDOI, spec}, nil
	}
	if arxivNewPattern.MatchString(spec) || arxivPrefix.MatchString(spec) {
		return itemRef{refArxiv, spec}, nil
	}
	if isbn, ok := normalizeISBN(spec); ok {
		return itemRef{refISBN, isbn}, nil
	}
	if lower := strings.ToLower(spec); catalogueKey.MatchString(spec) &&
		!strings.HasPrefix(lower, "http:") && !strings.HasPrefix(lower, "https:") {
		return itemRef{refKey, spec}, nil
	}
	return itemRef{}, fmt.Errorf("item %s: not a work file that exists, a work id or URL, a DOI, "+
		"an arXiv id, an ISBN or a catalogue key (book:<key>)", spec)
}

// checkEdition accepts the three ways to name an edition: its expression id,
// its ISBN, or `<language>-<year>`. Only the last two mean the same on every
// deployment.
func checkEdition(spec string) (string, error) {
	switch {
	case uuidPattern.MatchString(spec):
		return strings.ToLower(spec), nil
	case langYear.MatchString(spec):
		return spec, nil
	}
	if isbn, ok := normalizeISBN(spec); ok {
		return isbn, nil
	}
	return "", fmt.Errorf("edition %q is not an expression id, an ISBN or <language>-<year> (ru-1976)", spec)
}

// carries says whether an edition, or one of its tomes, has this ISBN-13.
func carries(e api.Expression, isbn string) bool {
	if e.ISBN13 == isbn {
		return true
	}
	return slices.ContainsFunc(e.Volumes, func(v api.Volume) bool { return v.ISBN13 == isbn })
}

// resolveEdition turns an ISBN or `<language>-<year>` edition into the id of
// the one edition of the work it names. None, or several, is an error that
// lists the work's editions.
func resolveEdition(ctx context.Context, client *api.Client, workID, spec string) (string, error) {
	if uuidPattern.MatchString(spec) {
		return strings.ToLower(spec), nil
	}
	exprs, err := client.ListExpressions(ctx, workID)
	if err != nil {
		return "", err
	}
	var hits []api.Expression
	for _, e := range exprs {
		if m := langYear.FindStringSubmatch(spec); m != nil {
			if e.Language == m[1] && strconv.Itoa(e.Year) == m[2] {
				hits = append(hits, e)
			}
		} else if carries(e, spec) {
			hits = append(hits, e)
		}
	}
	if len(hits) == 1 {
		return strings.ToLower(hits[0].ID), nil
	}
	var have []string
	for _, e := range exprs {
		have = append(have, fmt.Sprintf("%s-%d %q (%s)", e.Language, e.Year, e.Label, e.ID))
	}
	what := "matches no edition"
	if len(hits) > 1 {
		what = fmt.Sprintf("matches %d editions", len(hits))
	}
	if len(have) == 0 {
		have = []string{"none"}
	}
	return "", fmt.Errorf("edition %s %s of the work; its editions: %s", spec, what, strings.Join(have, "; "))
}

// normalizeISBN returns the ISBN-13 for a valid ISBN-10 or ISBN-13.
func normalizeISBN(raw string) (string, bool) {
	s := isbnPrefix.ReplaceAllString(strings.TrimSpace(raw), "")
	s = strings.NewReplacer("-", "", " ", "").Replace(s)
	switch len(s) {
	case 10:
		sum := 0
		for i := 0; i < 10; i++ {
			c := s[i]
			var v int
			switch {
			case c >= '0' && c <= '9':
				v = int(c - '0')
			case (c == 'X' || c == 'x') && i == 9:
				v = 10
			default:
				return "", false
			}
			sum += v * (10 - i)
		}
		if sum%11 != 0 {
			return "", false
		}
		return isbn13From("978" + s[:9]), true
	case 13:
		if !strings.HasPrefix(s, "978") && !strings.HasPrefix(s, "979") {
			return "", false
		}
		for _, c := range s {
			if c < '0' || c > '9' {
				return "", false
			}
		}
		if isbn13From(s[:12]) != s {
			return "", false
		}
		return s, true
	}
	return "", false
}

// isbn13From appends the check digit to 12 digits.
func isbn13From(twelve string) string {
	sum := 0
	for i := 0; i < 12; i++ {
		d := int(twelve[i] - '0')
		if i%2 == 1 {
			d *= 3
		}
		sum += d
	}
	return twelve + string(rune('0'+(10-sum%10)%10))
}

// resolveItems finds the work each entry names. Everything is classified, and
// every file read, before the first request, so a malformed manifest fails
// without touching the API. A work the site does not hold is skipped; a name
// that matches several works fails the manifest and lists them.
func resolveItems(ctx context.Context, client *api.Client, manifest string, entries []manifestEntry) ([]itemResult, error) {
	dir := filepath.Dir(manifest)
	results := make([]itemResult, len(entries))
	refs := make([]itemRef, len(entries))
	seenKey := map[string]string{}

	for i, entry := range entries {
		spec := entry.spec
		ref, err := classifyItem(dir, spec.Work)
		if err != nil {
			if strings.HasPrefix(err.Error(), "item ") {
				return nil, err
			}
			return nil, fmt.Errorf("item %d: %w", i+1, err)
		}
		refs[i] = ref
		results[i] = itemResult{Path: spec.Work, note: spec.Note, pin: spec.Edition, section: entry.section}
		if spec.Edition != "" {
			pin, err := checkEdition(spec.Edition)
			if err != nil {
				return nil, fmt.Errorf("item %s: %w", spec.Work, err)
			}
			results[i].pin = pin
		}
		if len(spec.Note) > 2000 {
			return nil, fmt.Errorf("item %s: a note is at most 2000 characters", spec.Work)
		}
		if ref.kind != refFile {
			continue
		}
		raw, err := os.ReadFile(ref.value)
		if err != nil {
			return nil, fmt.Errorf("item %s: %w", spec.Work, err)
		}
		meta, _, err := frontmatter.Split[frontmatter.Meta](string(raw))
		if err != nil {
			return nil, fmt.Errorf("item %s: %w", spec.Work, err)
		}
		key := strings.TrimSpace(meta.ImportKey)
		if key == "" {
			key = defaultImportKey(ref.value)
		}
		if first, dup := seenKey[key]; dup {
			return nil, fmt.Errorf("items %s and %s share the import_key %q; list each work once",
				first, spec.Work, key)
		}
		seenKey[key] = spec.Work
		results[i].ImportKey = key
	}

	for i := range results {
		r := &results[i]
		var err error
		switch refs[i].kind {
		case refFile:
			err = resolveFile(ctx, client, r)
		case refID:
			err = resolveByID(ctx, client, r, refs[i].value)
		case refDOI, refArxiv:
			err = resolveByIdentifier(ctx, client, r)
		case refISBN:
			err = resolveByISBN(ctx, client, r, refs[i].value)
		case refKey:
			err = resolveByKey(ctx, client, r, refs[i].value)
		}
		if err == nil && r.pin != "" && r.Action != itemSkip && r.PublicationID != "" {
			r.pin, err = resolveEdition(ctx, client, r.PublicationID, r.pin)
		}
		if err != nil {
			return nil, fmt.Errorf("item %s: %w", r.Path, err)
		}
	}

	seenID := map[string]string{}
	for _, r := range results {
		if r.PublicationID == "" || r.Action == itemSkip {
			continue
		}
		if first, dup := seenID[r.PublicationID]; dup {
			return nil, fmt.Errorf("items %s and %s are the same work; list each work once", first, r.Path)
		}
		seenID[r.PublicationID] = r.Path
	}
	return results, nil
}

func resolveFile(ctx context.Context, client *api.Client, r *itemResult) error {
	ref, err := client.FindPublicationByImportKey(ctx, r.ImportKey)
	if err != nil {
		return err
	}
	if ref == nil {
		r.Action, r.Reason = itemSkip, skipNotPublished
		return nil
	}
	// Kept on a skipped item too, so one that was added and later deleted is
	// recognised in the collection rather than counted as an item the
	// manifest does not name.
	r.PublicationID = strings.ToLower(ref.ID)
	switch {
	case ref.Removed:
		r.Action, r.Reason = itemSkip, skipDeleted
	case ref.IsDraft:
		r.Action, r.Reason = itemSkip, skipDraft
	}
	return nil
}

func resolveByID(ctx context.Context, client *api.Client, r *itemResult, id string) error {
	w, err := client.GetWork(ctx, id)
	if err != nil {
		if api.IsNotFound(err) {
			r.Action, r.Reason = itemSkip, skipNotFound
			return nil
		}
		return err
	}
	r.PublicationID, r.Title = id, w.Title
	switch {
	case w.Removed:
		r.Action, r.Reason = itemSkip, skipDeleted
	case w.IsDraft:
		r.Action, r.Reason = itemSkip, skipDraft
	}
	return nil
}

// resolveByIdentifier uses only `indexed`, a work the site holds. `resolved`
// is a record fetched from Crossref or arXiv that nothing stores, and adding
// it would mean creating a work.
func resolveByIdentifier(ctx context.Context, client *api.Client, r *itemResult) error {
	res, err := client.ResolveWork(ctx, r.Path)
	if err != nil {
		return err
	}
	if res.Indexed == nil {
		r.Action, r.Reason = itemSkip, skipNotIndexed
		return nil
	}
	r.PublicationID, r.Title = strings.ToLower(res.Indexed.ID), res.Indexed.Title
	return nil
}

// resolveByKey finds a library book by its catalogue key. A key no public
// work holds is skipped, like an identifier the site does not hold.
func resolveByKey(ctx context.Context, client *api.Client, r *itemResult, key string) error {
	ref, err := client.FindWorkByKey(ctx, key)
	if err != nil {
		return err
	}
	if ref == nil {
		r.Action, r.Reason = itemSkip, skipNotIndexed
		return nil
	}
	r.PublicationID, r.Title = strings.ToLower(ref.ID), ref.Title
	return nil
}

// resolveByISBN finds the work whose edition carries this ISBN, and pins that
// edition unless the manifest names one.
func resolveByISBN(ctx context.Context, client *api.Client, r *itemResult, isbn string) error {
	res, err := client.ResolveWork(ctx, isbn)
	if err != nil {
		return err
	}
	var cards []api.WorkCardRef
	if res.Indexed != nil {
		cards = append(cards, *res.Indexed)
	}
	for _, c := range res.Suggestions {
		if !slices.ContainsFunc(cards, func(o api.WorkCardRef) bool { return o.ID == c.ID }) {
			cards = append(cards, c)
		}
	}
	if len(cards) == 0 {
		r.Action, r.Reason = itemSkip, skipNotIndexed
		return nil
	}
	type hit struct {
		card api.WorkCardRef
		expr string
	}
	var hits []hit
	for _, card := range cards {
		exprs, err := client.ListExpressions(ctx, card.ID)
		if err != nil {
			return err
		}
		for _, e := range exprs {
			if carries(e, isbn) {
				hits = append(hits, hit{card, strings.ToLower(e.ID)})
			}
		}
	}
	switch {
	case len(hits) == 1:
		r.PublicationID, r.Title = strings.ToLower(hits[0].card.ID), hits[0].card.Title
		if r.pin == "" {
			r.pin = hits[0].expr
		}
	case len(hits) == 0 && res.Indexed != nil:
		// The site holds a work for this ISBN but lists no edition with it:
		// the work, any edition.
		r.PublicationID, r.Title = strings.ToLower(res.Indexed.ID), res.Indexed.Title
	case len(hits) == 0:
		r.Action, r.Reason = itemSkip, skipNotIndexed
	default:
		var lines []string
		for _, h := range hits {
			lines = append(lines, fmt.Sprintf("%s %q (edition %s)", h.card.ID, h.card.Title, h.expr))
		}
		return fmt.Errorf("ISBN %s matches %d editions; name the work by id or URL: %s",
			isbn, len(hits), strings.Join(lines, "; "))
	}
	return nil
}

// --- order ----------------------------------------------------------------

// orderGroup is one section's work ids, in order. key "" is the preamble; an
// existing section's key is its id and a section this run creates is
// "new:<title>".
type orderGroup struct {
	key string
	ids []string
}

// fillSlots returns cur with the places held by wanted items refilled in
// wanted order, and any wanted item cur did not hold appended. Items of cur
// that are not wanted keep their places.
func fillSlots(cur, wanted []string) []string {
	want := make(map[string]bool, len(wanted))
	for _, id := range wanted {
		want[id] = true
	}
	out := make([]string, 0, len(cur)+len(wanted))
	next := 0
	for _, id := range cur {
		if want[id] && next < len(wanted) {
			out = append(out, wanted[next])
			next++
		} else {
			out = append(out, id)
		}
	}
	return append(out, wanted[next:]...)
}

// planOrder computes the order the collection should be in (final) and the
// order it will be in once the missing works are appended (actual). They
// differ exactly when a reorder has to be sent.
//
// wantSection[i] is the group key of manifest section i. Works the manifest
// does not name keep their places, and a section it does not name stays.
func planOrder(sections []api.CollectionSection, items []api.CollectionItem,
	listed []itemResult, wantSection []string) (final, actual []orderGroup) {

	sorted := slices.Clone(sections)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Position < sorted[j].Position })
	var curOrder []string
	for _, s := range sorted {
		curOrder = append(curOrder, s.ID)
	}

	ordered := slices.Clone(items)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Position < ordered[j].Position })
	curGroups := map[string][]string{}
	present := map[string]bool{}
	for _, it := range ordered {
		id := strings.ToLower(it.EntityID)
		present[id] = true
		key := strings.ToLower(it.SectionID)
		curGroups[key] = append(curGroups[key], id)
	}

	groupOf := func(r itemResult) string {
		if r.section < 0 {
			return ""
		}
		return wantSection[r.section]
	}
	wantIn := map[string][]string{}
	moved := map[string]string{} // work id -> group it should be in
	for _, r := range listed {
		if r.Action == itemSkip {
			continue
		}
		g := groupOf(r)
		wantIn[g] = append(wantIn[g], r.PublicationID)
		moved[r.PublicationID] = g
	}

	var manifestKeys []string
	for _, k := range wantSection {
		if !slices.Contains(manifestKeys, k) {
			manifestKeys = append(manifestKeys, k)
		}
	}
	sectionOrder := fillSlots(curOrder, manifestKeys)
	actualOrder := slices.Clone(curOrder)
	for _, k := range manifestKeys {
		if !slices.Contains(actualOrder, k) {
			actualOrder = append(actualOrder, k)
		}
	}

	keys := append([]string{""}, sectionOrder...)
	for _, key := range keys {
		var keep []string
		for _, id := range curGroups[key] {
			if g, named := moved[id]; !named || g == key {
				keep = append(keep, id)
			}
		}
		final = append(final, orderGroup{key, fillSlots(keep, wantIn[key])})
	}
	for _, key := range append([]string{""}, actualOrder...) {
		ids := slices.Clone(curGroups[key])
		for _, r := range listed {
			if r.Action == itemSkip || present[r.PublicationID] || groupOf(r) != key {
				continue
			}
			ids = append(ids, r.PublicationID)
		}
		actual = append(actual, orderGroup{key, ids})
	}
	return dropEmptyPreamble(final), dropEmptyPreamble(actual)
}

func dropEmptyPreamble(g []orderGroup) []orderGroup {
	if len(g) > 0 && g[0].key == "" && len(g[0].ids) == 0 {
		return g[1:]
	}
	return g
}

func sameOrder(a, b []orderGroup) bool {
	return slices.EqualFunc(a, b, func(x, y orderGroup) bool {
		return x.key == y.key && slices.Equal(x.ids, y.ids)
	})
}
