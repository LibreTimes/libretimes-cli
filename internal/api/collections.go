package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

// CollectionTypeValues are the kinds of collection the platform has: a plain
// curated list, a course (one lecture course's material in order), and a
// recommendation list. Named once so validation and help text cannot drift; the
// API is the authority.
var CollectionTypeValues = []string{"collection", "course", "recommendation", "reading_list"}

// CollectionVisibilityValues is a collection's visibility, which is NOT a
// publication's: collections take the four audience levels and have no share
// links, so `private` and `by_link` are refused here while `owner` is accepted.
var CollectionVisibilityValues = []string{"public", "authenticated", "connections", "owner"}

// CollectionInput is the set of collection fields lt writes.
//
// omitempty throughout, for the reason PublicationInput gives: on a PATCH a
// zero value would clear a field the file never mentioned.
type CollectionInput struct {
	Title           string   `json:"title,omitempty"`
	Description     string   `json:"description,omitempty"`
	PrimaryLanguage string   `json:"primary_language,omitempty"`
	Visibility      string   `json:"visibility,omitempty"`
	CollectionType  string   `json:"collection_type,omitempty"`
	CategoryIDs     []string `json:"category_ids,omitempty"`
	Keywords        []string `json:"keywords,omitempty"`
	PublicComments  *bool    `json:"public_comments,omitempty"`
	// ImportKey is create-only. The update body has no such field, so the key
	// cannot be moved once set.
	ImportKey string `json:"import_key,omitempty"`
}

// CollectionRef is what the import-key lookup returns: enough to decide
// create-or-update, and not the collection's contents.
//
// There is no `removed` here, unlike PublicationRef. Deleting a collection
// deletes the row and frees its key, so a re-run after a deletion simply creates
// again.
type CollectionRef struct {
	ID             string `json:"id"`
	ImportKey      string `json:"import_key"`
	Title          string `json:"title"`
	CollectionType string `json:"collection_type"`
	Visibility     string `json:"visibility"`
	ItemsCount     int    `json:"items_count"`
}

// Collection is the subset of a created or updated collection lt reports back.
type Collection struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	ImportKey string `json:"import_key"`
}

// CollectionItem is one entry in a collection. Only publications exist today.
type CollectionItem struct {
	EntityType   string `json:"entity_type"`
	EntityID     string `json:"entity_id"`
	Position     int    `json:"position"`
	Note         string `json:"note,omitempty"`
	SectionID    string `json:"section_id,omitempty"`
	ExpressionID string `json:"expression_id,omitempty"`
	Title        string `json:"title,omitempty"`
}

// CollectionSection is a heading that groups items.
type CollectionSection struct {
	ID          string `json:"id"`
	Position    int    `json:"position"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	ItemsCount  int    `json:"items_count"`
}

// SectionInput is the body of a section create or update.
type SectionInput struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

// ItemIn is one work to add, with its note, section and pinned edition.
type ItemIn struct {
	EntityID     string `json:"entity_id"`
	EntityType   string `json:"entity_type"`
	Note         string `json:"note,omitempty"`
	SectionID    string `json:"section_id,omitempty"`
	ExpressionID string `json:"expression_id,omitempty"`
}

// OrderGroup is one section's items in the section-aware reorder. An empty
// SectionID is the unsectioned preamble.
type OrderGroup struct {
	SectionID *string  `json:"section_id"`
	EntityIDs []string `json:"entity_ids"`
}

type collectionItemPage struct {
	Items []CollectionItem `json:"items"`
	Total int              `json:"total"`
}

// itemsPageLimit is the API's maximum page size. A course is a dozen or two
// lectures, so this is one request in practice.
const itemsPageLimit = 100

// FindCollectionByImportKey looks up one of the caller's own collections by its
// import key. Returns (nil, nil) when the caller has never imported under it --
// the route is owner-scoped, so a 404 means exactly that.
func (c *Client) FindCollectionByImportKey(ctx context.Context, key string) (*CollectionRef, error) {
	var ref CollectionRef
	err := c.Get(ctx, "/collections/me/by-import-key", url.Values{"key": {key}}, &ref)
	if err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &ref, nil
}

// CreateCollection posts a new collection.
func (c *Client) CreateCollection(ctx context.Context, in CollectionInput) (*Collection, error) {
	var out Collection
	if err := c.Post(ctx, "/collections", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateCollection patches an existing collection by id.
func (c *Client) UpdateCollection(ctx context.Context, id string, in CollectionInput) (*Collection, error) {
	in.ImportKey = ""
	var out Collection
	if err := c.Patch(ctx, "/collections/"+url.PathEscape(id), in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteCollection deletes one of the caller's collections.
func (c *Client) DeleteCollection(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/collections/"+url.PathEscape(id), nil, nil, nil)
}

// ListCollectionItems returns every item in the collection, in position order.
func (c *Client) ListCollectionItems(ctx context.Context, id string) ([]CollectionItem, error) {
	path := "/collections/" + url.PathEscape(id) + "/items"
	var all []CollectionItem
	for offset := 0; ; {
		var page collectionItemPage
		query := url.Values{
			"limit":  {strconv.Itoa(itemsPageLimit)},
			"offset": {strconv.Itoa(offset)},
		}
		if err := c.Get(ctx, path, query, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		offset += len(page.Items)
		if len(page.Items) == 0 || offset >= page.Total {
			return all, nil
		}
	}
}

// AddWorksToCollection appends works with their notes, sections and pins, in
// the order given. The API skips any already present, so a re-send adds nothing.
func (c *Client) AddWorksToCollection(ctx context.Context, id string, items []ItemIn) error {
	for i := range items {
		items[i].EntityType = "work"
	}
	body := struct {
		Items []ItemIn `json:"items"`
	}{Items: items}
	return c.Post(ctx, "/collections/"+url.PathEscape(id)+"/items", body, nil)
}

// AddPublicationsToCollection appends works by id.
func (c *Client) AddPublicationsToCollection(ctx context.Context, id string, publicationIDs []string) error {
	items := make([]ItemIn, 0, len(publicationIDs))
	for _, pid := range publicationIDs {
		items = append(items, ItemIn{EntityID: pid})
	}
	return c.AddWorksToCollection(ctx, id, items)
}

// UpdateCollectionItem patches one work's note and/or pinned edition. Only the
// keys present in fields are sent.
func (c *Client) UpdateCollectionItem(ctx context.Context, id, workID string, fields map[string]any) error {
	return c.Patch(ctx, "/collections/"+url.PathEscape(id)+"/items/work/"+url.PathEscape(workID), fields, nil)
}

// ReorderCollection sets each work's position to its index in the list.
// Items absent from the list keep their positions.
func (c *Client) ReorderCollection(ctx context.Context, id string, workIDs []string) error {
	body := struct {
		EntityIDs  []string `json:"entity_ids"`
		EntityType string   `json:"entity_type"`
	}{EntityIDs: workIDs, EntityType: "work"}
	return c.do(ctx, http.MethodPut, "/collections/"+url.PathEscape(id)+"/items/order", nil, body, nil)
}

// ReorderCollectionSections puts every listed section's items in order, moves
// them between sections, and orders the sections by the groups' order.
func (c *Client) ReorderCollectionSections(ctx context.Context, id string, groups []OrderGroup) error {
	body := struct {
		Sections   []OrderGroup `json:"sections"`
		EntityType string       `json:"entity_type"`
	}{Sections: groups, EntityType: "work"}
	return c.do(ctx, http.MethodPut, "/collections/"+url.PathEscape(id)+"/items/order", nil, body, nil)
}

// CreateSection appends a section.
func (c *Client) CreateSection(ctx context.Context, id string, in SectionInput) (*CollectionSection, error) {
	var out CollectionSection
	if err := c.Post(ctx, "/collections/"+url.PathEscape(id)+"/sections", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateSection patches a section's title or description.
func (c *Client) UpdateSection(ctx context.Context, id, sectionID string, in SectionInput) error {
	return c.Patch(ctx, "/collections/"+url.PathEscape(id)+"/sections/"+url.PathEscape(sectionID), in, nil)
}

// ExportCollection reads the list as a CSL-JSON array.
func (c *Client) ExportCollection(ctx context.Context, id, lang string) ([]byte, error) {
	query := url.Values{}
	if lang != "" {
		query.Set("lang", lang)
	}
	var raw json.RawMessage
	if err := c.Get(ctx, "/collections/"+url.PathEscape(id)+"/export", query, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// CollectionSummary is one of the caller's collections as the list and the
// single read report it.
type CollectionSummary struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	CollectionType  string `json:"collection_type"`
	Visibility      string `json:"visibility"`
	PrimaryLanguage string `json:"primary_language,omitempty"`
	ItemsCount      int    `json:"items_count"`
	ImportKey       string `json:"import_key,omitempty"`
	// Sections is present on the single read only.
	Sections []CollectionSection `json:"sections,omitempty"`
}

type collectionPage struct {
	Items   []CollectionSummary `json:"items"`
	HasMore bool                `json:"has_more"`
}

// GetCollection reads one collection.
func (c *Client) GetCollection(ctx context.Context, id string) (*CollectionSummary, error) {
	var out CollectionSummary
	if err := c.Get(ctx, "/collections/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListMyCollections returns every collection the caller owns, at every
// visibility level.
func (c *Client) ListMyCollections(ctx context.Context) ([]CollectionSummary, error) {
	var all []CollectionSummary
	for offset := 0; ; {
		var page collectionPage
		query := url.Values{
			"limit":  {strconv.Itoa(itemsPageLimit)},
			"offset": {strconv.Itoa(offset)},
		}
		if err := c.Get(ctx, "/collections/me", query, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		offset += len(page.Items)
		if !page.HasMore || len(page.Items) == 0 {
			return all, nil
		}
	}
}
