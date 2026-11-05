package httpapi_test

import (
	"encoding/csv"
	"os"
	"regexp"
	"testing"

	"novalogistik/backend/internal/httpapi"
	"novalogistik/backend/internal/tenant"
)

func TestInventoryMatchesRegisteredRoutes(t *testing.T) {
	registry := tenant.New(tenant.Config{})
	defer registry.Close()
	app := httpapi.NewWithTenants(nil, registry)
	routes := map[string]bool{}
	parameter := regexp.MustCompile(`:([a-zA-Z][a-zA-Z0-9_]*)`)
	for _, route := range app.GetRoutes(true) {
		routes[route.Method+" "+parameter.ReplaceAllString(route.Path, `[$1]`)] = true
	}
	file, err := os.Open("../../docs/route-inventory.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	implemented := 0
	for _, row := range rows[1:] {
		key := row[1] + " " + row[2]
		registered := routes[key]
		if row[0] >= "1" && row[0] <= "5" && !registered {
			t.Errorf("phase %s has an unmigrated route: %s", row[0], key)
		}
		if (row[4] == "implemented") != registered {
			t.Errorf("inventory disagrees with registered Fiber route: %s (status %s)", key, row[4])
		}
		if registered {
			implemented++
		}
	}
	if implemented != 104 {
		t.Fatalf("expected 104 implemented legacy handlers, got %d", implemented)
	}
}
