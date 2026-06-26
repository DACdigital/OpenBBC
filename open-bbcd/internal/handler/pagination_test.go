package handler

import (
	"net/http/httptest"
	"testing"
)

func TestParsePageRequest_Defaults(t *testing.T) {
	req := httptest.NewRequest("GET", "/x", nil)
	got := ParsePageRequest(req)
	if got.Page != 1 || got.Size != defaultPageSize {
		t.Fatalf("defaults wrong: %+v", got)
	}
}

func TestParsePageRequest_ClampsAndFallsBack(t *testing.T) {
	cases := []struct {
		url           string
		wantPage      int
		wantSize      int
		wantOffset    int
	}{
		{"/x?page=3&size=20", 3, 20, 40},
		{"/x?page=-5&size=10", 1, 10, 0},     // negative page → 1
		{"/x?size=999", 1, maxPageSize, 0},   // size > max → clamp
		{"/x?size=0", 1, defaultPageSize, 0}, // size 0 → default
		{"/x?page=abc", 1, defaultPageSize, 0}, // garbage → defaults
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", c.url, nil)
		got := ParsePageRequest(req)
		if got.Page != c.wantPage || got.Size != c.wantSize || got.Offset() != c.wantOffset {
			t.Errorf("%s: got page=%d size=%d offset=%d; want page=%d size=%d offset=%d",
				c.url, got.Page, got.Size, got.Offset(), c.wantPage, c.wantSize, c.wantOffset)
		}
	}
}

func TestNewPageView_Math(t *testing.T) {
	cases := []struct {
		page, size, total int
		wantPages         int
		wantHasPrev       bool
		wantHasNext       bool
	}{
		{1, 50, 0, 1, false, false},
		{1, 50, 50, 1, false, false},
		{1, 50, 51, 2, false, true},
		{2, 50, 100, 2, true, false},
		{2, 50, 150, 3, true, true},
	}
	for _, c := range cases {
		got := NewPageView(PageRequest{Page: c.page, Size: c.size}, c.total)
		if got.TotalPages != c.wantPages || got.HasPrev != c.wantHasPrev || got.HasNext != c.wantHasNext {
			t.Errorf("page=%d size=%d total=%d → got %+v; want pages=%d prev=%v next=%v",
				c.page, c.size, c.total, got, c.wantPages, c.wantHasPrev, c.wantHasNext)
		}
	}
}
