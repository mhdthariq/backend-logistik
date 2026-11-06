// Package handler provides the Vercel Function entry point.
package handler

import (
	"net/http"

	"novalogistik/backend/vercelhandler"
)

// Handler uses a public boundary because Vercel rewrites this package's module name.
func Handler(w http.ResponseWriter, r *http.Request) {
	vercelhandler.Handler(w, r)
}
