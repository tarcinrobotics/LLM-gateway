package handlers

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"llm_gateway/internal/database"
)

func TestCreateAPIKeyRequestBodyLimit(t *testing.T) {
	db, err := database.InitDB(
		filepath.Join(t.TempDir(), "admin-body-limit.db"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := database.SeedData(db); err != nil {
		t.Fatal(err)
	}

	handler := CreateAPIKeyHandler(db)

	base := `{"user_id":1,"role_id":1,"rate_limit_rpm":60}`

	exact := base + strings.Repeat(
		" ",
		int(CreateAPIKeyRequestBodyLimit)-len(base),
	)

	request := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodPost,
			"/admin/keys",
			strings.NewReader(body),
		)

		req.ContentLength = -1

		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)

		return recorder
	}

	// Exactly at the limit must be accepted.
	if got := request(exact); got.Code != http.StatusCreated {
		t.Fatalf(
			"exact-limit admin body should succeed, got %d: %s",
			got.Code,
			got.Body.String(),
		)
	}

	// One byte above the limit must be rejected.
	if got := request(exact + " "); got.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf(
			"over-limit admin body should return 413, got %d: %s",
			got.Code,
			got.Body.String(),
		)
	}

	// Malformed JSON within the limit must remain HTTP 400.
	if got := request(`{"user_id":`); got.Code != http.StatusBadRequest {
		t.Fatalf(
			"malformed in-limit JSON should return 400, got %d: %s",
			got.Code,
			got.Body.String(),
		)
	}
}
