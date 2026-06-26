package handler

import (
	"net/http"
	"strconv"
)

// PageRequest is the parsed pagination state from a request's query string.
// Page is 1-based for human-readable URLs (?page=1 means first page).
// Size is the page size, defaulted+clamped to the configured bounds.
type PageRequest struct {
	Page int
	Size int
}

// PageView is the page metadata templates render (Prev/Next links + "page N of M").
// Computed by NewPageView once the total row count is known.
type PageView struct {
	Page       int
	Size       int
	Total      int
	TotalPages int
	HasPrev    bool
	HasNext    bool
}

const (
	defaultPageSize = 50
	maxPageSize     = 200
)

// ParsePageRequest reads ?page= and ?size= from r.URL.Query() with the
// canonical defaults (page=1, size=50, max size=200). Invalid values fall
// back to defaults rather than 400-ing — pagination is navigational.
func ParsePageRequest(r *http.Request) PageRequest {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(q.Get("size"))
	if size <= 0 {
		size = defaultPageSize
	}
	if size > maxPageSize {
		size = maxPageSize
	}
	return PageRequest{Page: page, Size: size}
}

// Offset returns the SQL OFFSET value for this page.
func (p PageRequest) Offset() int { return (p.Page - 1) * p.Size }

// Limit returns the SQL LIMIT value for this page.
func (p PageRequest) Limit() int { return p.Size }

// NewPageView builds the template-facing view from a request + total count.
// If page overshoots, HasNext is false; HasPrev follows page>1.
func NewPageView(req PageRequest, total int) PageView {
	totalPages := 1
	if req.Size > 0 {
		totalPages = (total + req.Size - 1) / req.Size
		if totalPages < 1 {
			totalPages = 1
		}
	}
	return PageView{
		Page:       req.Page,
		Size:       req.Size,
		Total:      total,
		TotalPages: totalPages,
		HasPrev:    req.Page > 1,
		HasNext:    req.Page < totalPages,
	}
}
