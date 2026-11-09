package api

import (
	"context"
	"net/url"
)

// A translation is one more language of the same work or collection -- a face
// of it, not a second item. The original language is the row's own text and is
// written through the ordinary update; these routes carry every other language.

// TranslationInput is one language's text. Pointers, because the API tells
// "not sent" (leave it) from an explicit empty value (clear it); lt only ever
// sends what the file states.
type TranslationInput struct {
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	Content     *string `json:"content,omitempty"`
}

// Translation is one language's stored text.
type Translation struct {
	Language    string  `json:"language"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Content     *string `json:"content"`
}

// TranslationLanguage is one line of the overview: how much of a language
// exists and whether readers are served it.
type TranslationLanguage struct {
	Language         string `json:"language"`
	LanguageName     string `json:"language_name"`
	TranslatedFields int    `json:"translated_fields"`
	TotalFields      int    `json:"total_fields"`
	IsPublished      bool   `json:"is_published"`
}

// TranslationsOverview is which languages a work or collection has.
type TranslationsOverview struct {
	PrimaryLanguage string                `json:"primary_language"`
	Languages       []TranslationLanguage `json:"languages"`
}

// TranslationTarget names what is being translated: a work or a collection,
// the two roots whose routes share this shape. A work's are `/works/{id}/texts`,
// a collection's `/collections/{id}/translations`.
type TranslationTarget string

const (
	TranslateWork       TranslationTarget = "works"
	TranslateCollection TranslationTarget = "collections"
)

func translationsPath(target TranslationTarget, id string) string {
	tail := "/translations"
	if target == TranslateWork {
		tail = "/texts"
	}
	return "/" + string(target) + "/" + url.PathEscape(id) + tail
}

// GetTranslations reads the overview.
func (c *Client) GetTranslations(ctx context.Context, target TranslationTarget, id string) (*TranslationsOverview, error) {
	var out TranslationsOverview
	if err := c.Get(ctx, translationsPath(target, id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetTranslation reads one language. Returns (nil, nil) when there is none.
func (c *Client) GetTranslation(ctx context.Context, target TranslationTarget, id, language string) (*Translation, error) {
	var out Translation
	err := c.Get(ctx, translationsPath(target, id)+"/"+url.PathEscape(language), nil, &out)
	if err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}

// PutTranslation creates or updates one language.
func (c *Client) PutTranslation(ctx context.Context, target TranslationTarget, id, language string, in TranslationInput) (*Translation, error) {
	var out Translation
	if err := c.Patch(ctx, translationsPath(target, id)+"/"+url.PathEscape(language), in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteTranslation removes one language.
func (c *Client) DeleteTranslation(ctx context.Context, target TranslationTarget, id, language string) error {
	return c.Delete(ctx, translationsPath(target, id)+"/"+url.PathEscape(language))
}
