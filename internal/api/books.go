package api

import (
	"context"
	"net/http"
	"net/url"
)

// TypeBook is the publication type whose text is a tree of divisions rather
// than one body. A book is created as a draft with no `content`; its chapters
// are written through the division routes below, and it is published once one
// of them has text.
const TypeBook = "book"

// DivisionKinds is the platform's closed set of division kinds, named once so
// validation and help text cannot drift from each other. The API is the
// authority; this copy only lets lt name a bad kind before any request.
var DivisionKinds = []string{
	"volume", "book", "part", "chapter", "section", "article", "poem",
	"story", "letter", "act", "scene", "front_matter", "back_matter",
}

// BookInfo is the subset of a publication read that `lt book` decides on.
type BookInfo struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Language string `json:"language"`
	IsDraft  bool   `json:"is_draft"`
	// Which expression holds the tree for each language. An authored book has
	// one entry until it is translated.
	PrimaryExpressionByLanguage map[string]string `json:"primary_expression_by_language"`
}

// GetPublication reads one publication. The caller's own drafts are visible to
// it, as they are on the site.
func (c *Client) GetPublication(ctx context.Context, id string) (*BookInfo, error) {
	var out BookInfo
	if err := c.Get(ctx, "/publications/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DivisionNode is one node of the contents tree. It carries no body: the tree
// is what the reader's rail reads, and a 60-chapter book's text is not.
type DivisionNode struct {
	ID            string         `json:"id"`
	Kind          string         `json:"kind"`
	Label         string         `json:"label"`
	Title         *string        `json:"title"`
	FirstLine     *string        `json:"first_line"`
	ReadingNumber *int           `json:"reading_number"`
	Ref           *string        `json:"ref"`
	Children      []DivisionNode `json:"children"`
}

// Contents is one expression's division tree.
type Contents struct {
	ExpressionID string         `json:"expression_id"`
	Tree         []DivisionNode `json:"tree"`
}

// GetContents reads the division tree of one expression of a book.
func (c *Client) GetContents(ctx context.Context, workID, expressionID string) (*Contents, error) {
	var out Contents
	query := url.Values{"e": {expressionID}}
	if err := c.Get(ctx, "/works/"+url.PathEscape(workID)+"/contents", query, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Division is one division as the editor holds it, body included.
type Division struct {
	ID            string  `json:"id"`
	ParentID      *string `json:"parent_id"`
	Kind          string  `json:"kind"`
	Label         string  `json:"label"`
	Title         *string `json:"title"`
	FirstLine     *string `json:"first_line"`
	ReadingNumber *int    `json:"reading_number"`
	Ref           *string `json:"ref"`
	BodyMD        *string `json:"body_md"`
}

// DivisionCreate is a new node. It has no body: a node is navigation until a
// body is written to it with UpdateDivision.
type DivisionCreate struct {
	Kind      string  `json:"kind"`
	Label     string  `json:"label"`
	Title     *string `json:"title,omitempty"`
	FirstLine *string `json:"first_line,omitempty"`
	Ref       *string `json:"ref,omitempty"`
	ParentID  *string `json:"parent_id"`
	Index     int     `json:"index"`
}

// DivisionMove places a node under ParentID (nil for the root level) at Index
// among the siblings there, the node itself not counted.
type DivisionMove struct {
	ParentID *string `json:"parent_id"`
	Index    int     `json:"index"`
}

func divisionsPath(workID, expressionID string) string {
	return "/works/" + url.PathEscape(workID) + "/expressions/" + url.PathEscape(expressionID) + "/divisions"
}

// GetDivision reads one division for editing, body and all.
func (c *Client) GetDivision(ctx context.Context, workID, expressionID, id string) (*Division, error) {
	var out Division
	if err := c.Get(ctx, divisionsPath(workID, expressionID)+"/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateDivision adds a node.
func (c *Client) CreateDivision(ctx context.Context, workID, expressionID string, in DivisionCreate) (*Division, error) {
	var out Division
	if err := c.Post(ctx, divisionsPath(workID, expressionID), in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateDivision patches a node. The body is a map rather than a struct
// because the route tells a field left out (unchanged) from a field sent as
// null (cleared), and a title removed from a manifest has to be sent as null.
func (c *Client) UpdateDivision(ctx context.Context, workID, expressionID, id string, fields map[string]any) (*Division, error) {
	var out Division
	if err := c.Patch(ctx, divisionsPath(workID, expressionID)+"/"+url.PathEscape(id), fields, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// MoveDivision re-parents and/or reorders a node with its subtree.
func (c *Client) MoveDivision(ctx context.Context, workID, expressionID, id string, in DivisionMove) error {
	return c.Post(ctx, divisionsPath(workID, expressionID)+"/"+url.PathEscape(id)+"/move", in, nil)
}

// DeleteDivision removes a node and its subtree. On a published book the
// reading numbers it held are retired, never reissued.
func (c *Client) DeleteDivision(ctx context.Context, workID, expressionID, id string) error {
	return c.do(ctx, http.MethodDelete, divisionsPath(workID, expressionID)+"/"+url.PathEscape(id), nil, nil, nil)
}
