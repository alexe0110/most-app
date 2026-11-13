// Package web serves the public pages (home, privacy policy, terms) that
// Google OAuth verification requires on the production domain.
package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
)

//go:embed templates/*.html
var templatesFS embed.FS

// botURL is fixed rather than built from TELEGRAM_BOT_USERNAME: the pages
// describe the production bot whatever the env (dev runs its own bot), and
// Google reviewers must see a working link.
const botURL = "https://t.me/most_files_bot"

// contactEmail is a constant, not config: the address is public by design and
// changes rarely, so a redeploy is cheaper than one more required env var.
const contactEmail = "alexe0110@gmail.com"

// contentSecurityPolicy forbids scripts, frames and any other origin: the pages
// are static text with inline styles only.
const contentSecurityPolicy = "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

// cacheControl lets browsers and proxies reuse the pages; the text changes rarely.
const cacheControl = "public, max-age=3600"

// Page paths.
const (
	pathHome    = "/{$}"
	pathPrivacy = "/privacy"
	pathTerms   = "/terms"
)

type pageData struct {
	ContactEmail string
	BotURL       string
}

// Pages serves pre-rendered public pages.
type Pages struct {
	home, privacy, terms []byte
}

// New renders all pages once. A template error is a startup error, so a broken
// page never reaches a request.
func New() (*Pages, error) {
	data := pageData{ContactEmail: contactEmail, BotURL: botURL}

	var p Pages
	for _, page := range []struct {
		name string
		dst  *[]byte
	}{
		{"index", &p.home},
		{"privacy", &p.privacy},
		{"terms", &p.terms},
	} {
		body, err := render(page.name, data)
		if err != nil {
			return nil, err
		}
		*page.dst = body
	}
	return &p, nil
}

// render parses the layout together with one page: every page defines the same
// blocks, so they cannot share one template set.
func render(name string, data pageData) ([]byte, error) {
	t, err := template.ParseFS(templatesFS, "templates/layout.html", "templates/"+name+".html")
	if err != nil {
		return nil, fmt.Errorf("parse %s template: %w", name, err)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		return nil, fmt.Errorf("render %s page: %w", name, err)
	}
	return buf.Bytes(), nil
}

// RegisterRoutes adds the page routes. GET patterns also answer HEAD.
func (p *Pages) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+pathHome, serve(p.home))
	mux.HandleFunc("GET "+pathPrivacy, serve(p.privacy))
	mux.HandleFunc("GET "+pathTerms, serve(p.terms))
}

func serve(body []byte) http.HandlerFunc {
	length := strconv.Itoa(len(body))
	return func(w http.ResponseWriter, _ *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", cacheControl)
		h.Set("Content-Length", length)
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		_, _ = w.Write(body) // the client went away; nothing to do
	}
}
