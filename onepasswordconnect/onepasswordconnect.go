// SPDX-License-Identifier: MIT
// Package onepasswordconnect implements a Docker credential helper backed by
// 1Password Connect (API + SDK). Mirrors the style used by other helpers.
package onepasswordconnect

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/docker/docker-credential-helpers/credentials"
	"github.com/docker/docker-credential-helpers/registryurl"

	"github.com/1Password/connect-sdk-go/connect"
	"github.com/1Password/connect-sdk-go/onepassword"
)

// Env keys (kept simple to mirror upstream helpers' style)
const (
	envConnectHost  = "OP_CONNECT_HOST"
	envConnectToken = "OP_CONNECT_TOKEN"
	envVault        = "OP_CONNECT_VAULT" // vault name or UUID (required)

	defaultTag = "docker-cred" // used to speed up List()
)

// Onepasswordconnect implements credentials.Helper for 1Password Connect.
type Onepasswordconnect struct{}

// Add stores/updates credentials for serverURL in OP Connect.
// It upserts a Login item titled "docker:<canonical-host>" with USERNAME/PASSWORD fields.
func (h Onepasswordconnect) Add(creds *credentials.Credentials) error {
	c, vaultID, err := clientAndVault()
	if err != nil {
		return err
	}

	host := canonicalHost(creds.ServerURL) // e.g., registry-1.docker.io
	title := "docker:" + host

	// Try find existing by title (fast path); fall back to nil when not found.
	existing, _ := getItemByTitle(c, vaultID, title)

	if existing == nil {
		item := &onepassword.Item{
			Title:    title,
			Category: onepassword.Login,
			Tags:     []string{defaultTag},
			URLs:     []*onepassword.ItemURL{{Href: storageURL(creds.ServerURL)}},
			Fields: []*onepassword.ItemField{
				{Purpose: "USERNAME", Value: creds.Username},
				{Purpose: "PASSWORD", Value: creds.Secret},
			},
		}
		_, err := c.CreateItem(item, vaultID)
		return err
	}

	// Replace core bits; use UpdateItem to avoid per-field ID juggling.
	existing.Tags = union(existing.Tags, []string{defaultTag})
	existing.URLs = []*onepassword.ItemURL{{Href: storageURL(creds.ServerURL)}}
	existing.Fields = []*onepassword.ItemField{
		{Purpose: "USERNAME", Value: creds.Username},
		{Purpose: "PASSWORD", Value: creds.Secret},
	}

	_, err = c.UpdateItem(existing, vaultID)
	return err
}

// Delete removes the stored credentials for serverURL.
func (h Onepasswordconnect) Delete(serverURL string) error {
	c, vaultID, err := clientAndVault()
	if err != nil {
		return err
	}
	title := "docker:" + canonicalHost(serverURL)
	it, err := getItemByTitle(c, vaultID, title)
	if err != nil {
		return err
	}
	if it == nil {
		return credentials.ErrCredentialsNotFound
	}
	return c.DeleteItem(it, vaultID)
}

// Get retrieves the username/secret for serverURL.
func (h Onepasswordconnect) Get(serverURL string) (string, string, error) {
	c, vaultID, err := clientAndVault()
	if err != nil {
		return "", "", err
	}
	title := "docker:" + canonicalHost(serverURL)
	it, err := getItemByTitle(c, vaultID, title)
	if err != nil {
		return "", "", err
	}
	if it == nil {
		return "", "", credentials.ErrCredentialsNotFound
	}
	var user, pass string
	for _, f := range it.Fields {
		switch f.Purpose {
		case "USERNAME":
			if f.Value != nil {
				user = fmt.Sprint(f.Value)
			}
		case "PASSWORD":
			if f.Value != nil {
				pass = fmt.Sprint(f.Value)
			}
		}
	}
	if user == "" && pass == "" {
		return "", "", credentials.ErrCredentialsNotFound
	}
	return user, pass, nil
}

// List returns server->username map for all docker-* items.
// We list by title prefix to avoid a full vault scan; if your Connect version
// exposes tag filters, that can be switched to tag=defaultTag.
func (h Onepasswordconnect) List() (map[string]string, error) {
	c, vaultID, err := clientAndVault()
	if err != nil {
		return nil, err
	}
	// The Connect SDK provides list helpers; fall back to list+filter if needed.
	items, err := c.GetItemsByTitlePrefix("docker:", vaultID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(items))
	for _, it := range items {
		if !strings.HasPrefix(it.Title, "docker:") {
			continue
		}
		host := strings.TrimPrefix(it.Title, "docker:")
		// Get full item to read fields (some SDK list calls return partials)
		full, ferr := c.GetItem(it.ID, vaultID)
		if ferr != nil || full == nil {
			continue
		}
		var user string
		for _, f := range full.Fields {
			if f.Purpose == "USERNAME" && f.Value != nil {
				user = fmt.Sprint(f.Value)
				break
			}
		}
		out[host] = user
	}
	return out, nil
}

// ---- internals ----

func clientAndVault() (connect.Client, string, error) {
	host := os.Getenv(envConnectHost)
	token := os.Getenv(envConnectToken)
	vq := os.Getenv(envVault)
	if host == "" || token == "" {
		return nil, "", errors.New("OP_CONNECT_HOST and OP_CONNECT_TOKEN are required")
	}
	if vq == "" {
		return nil, "", errors.New("OP_CONNECT_VAULT (name or UUID) is required")
	}
	c := connect.NewClient(host, token) // SDK constructs an authenticated client from env/args.  [oai_citation:1‡pkg.go.dev](https://pkg.go.dev/github.com/1Password/connect-sdk-go/connect?utm_source=chatgpt.com)
	// Resolve vault query (name or id) to an ID; the SDK allows using either.
	// If your SDK version already supports name queries directly, you can return vq.
	id, err := resolveVaultID(c, vq)
	return c, id, err
}

func resolveVaultID(c connect.Client, query string) (string, error) {
	// Fast-path: UUID provided
	if looksLikeUUID(query) {
		return query, nil
	}
	vs, err := c.GetVaults()
	if err != nil {
		return "", err
	}
	lq := strings.ToLower(query)
	for _, v := range vs {
		if strings.EqualFold(v.Name, query) || strings.ToLower(v.ID) == lq {
			return v.ID, nil
		}
	}
	return "", fmt.Errorf("vault not found: %s", query)
}

func looksLikeUUID(s string) bool {
	// Heuristic is fine; Connect will 4xx if wrong.
	return strings.Count(s, "-") == 4 && len(s) >= 36
}

func canonicalHost(serverURL string) string {
	// Use upstream helper’s canonicalization (Docker Hub quirks, /v1/)
	host := registryurl.ResolveNormalizedRegistry(serverURL)
	// Fallback in case ResolveNormalizedRegistry returns empty
	if host == "" {
		s := strings.TrimSpace(strings.ToLower(serverURL))
		s = strings.TrimPrefix(s, "https://")
		s = strings.TrimPrefix(s, "http://")
		s = strings.TrimSuffix(s, "/v1/")
		return s
	}
	return host
}

func storageURL(serverURL string) string {
	// Ensure a stable URL inside the item (helps UX/autofill in 1P).
	if strings.Contains(serverURL, "docker.io") {
		return "https://index.docker.io/v1/"
	}
	if strings.HasPrefix(serverURL, "http://") || strings.HasPrefix(serverURL, "https://") {
		return serverURL
	}
	return "https://" + serverURL
}

func union(a, b []string) []string {
	m := make(map[string]struct{}, len(a)+len(b))
	for _, s := range a {
		m[s] = struct{}{}
	}
	for _, s := range b {
		m[s] = struct{}{}
	}
	out := make([]string, 0, len(m))
	for s := range m {
		out = append(out, s)
	}
	return out
}

// ---- small query helpers (SDK-compat) ----

func getItemByTitle(c connect.Client, vaultID, title string) (*onepassword.Item, error) {
	// Prefer an SDK call that fetches by title if present; otherwise list & filter.
	items, err := c.GetItemsByTitle(title, vaultID)
	if err == nil && len(items) > 0 {
		// Some SDKs return partial items here; fetch full to ensure fields are present.
		return c.GetItem(items[0].ID, vaultID)
	}
	// Fallback: prefix search then exact match
	items, err = c.GetItemsByTitlePrefix("docker:", vaultID)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if it.Title == title {
			return c.GetItem(it.ID, vaultID)
		}
	}
	return nil, nil
}