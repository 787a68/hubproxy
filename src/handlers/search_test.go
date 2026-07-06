package handlers

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSearchCacheEvictsWhenFull(t *testing.T) {
	c := &Cache{data: make(map[string]cacheEntry), maxSize: 2}
	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3)

	if len(c.data) > c.maxSize {
		t.Fatalf("cache size = %d, want <= %d", len(c.data), c.maxSize)
	}
}

func TestParsePaginationParams(t *testing.T) {
	gin.SetMode(gin.TestMode)
	req := httptest.NewRequest("GET", "/search?page=3&page_size=50", nil)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req

	page, pageSize := parsePaginationParams(c, 25)
	if page != 3 || pageSize != 50 {
		t.Fatalf("page=%d pageSize=%d, want 3 and 50", page, pageSize)
	}
}
