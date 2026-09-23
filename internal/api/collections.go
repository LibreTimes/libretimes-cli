package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// CollectionTypeCourse is the only collection kind lt writes. A course is the
// ordered list of one lecture course's notes; other kinds are curated by hand in
// the editor.
const CollectionTypeCourse = "course"

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
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	Position   int    `json:"position"`
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

type itemIn struct {
	EntityID   string `json:"entity_id"`
	EntityType string `json:"entity_type"`
}

// AddPublicationsToCollection appends publications, in the order given. The API
// skips any already present, so a re-send adds nothing.
func (c *Client) AddPublicationsToCollection(ctx context.Context, id string, publicationIDs []string) error {
	body := struct {
		Items []itemIn `json:"items"`
	}{}
	for _, pid := range publicationIDs {
		body.Items = append(body.Items, itemIn{EntityID: pid, EntityType: "publication"})
	}
	return c.Post(ctx, "/collections/"+url.PathEscape(id)+"/items", body, nil)
}

// ReorderCollection sets each publication's position to its index in the list.
// Items absent from the list keep their positions.
func (c *Client) ReorderCollection(ctx context.Context, id string, publicationIDs []string) error {
	body := struct {
		EntityIDs  []string `json:"entity_ids"`
		EntityType string   `json:"entity_type"`
	}{EntityIDs: publicationIDs, EntityType: "publication"}
	return c.do(ctx, http.MethodPut, "/collections/"+url.PathEscape(id)+"/items/order", nil, body, nil)
}
