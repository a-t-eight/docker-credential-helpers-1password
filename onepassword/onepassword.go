// Package onepassword implements a 1Password-based credential helper.
// It stores Docker registry credentials as "login" items in 1Password via the
// `op` CLI (routed through 1Password Connect via OP_CONNECT_HOST / OP_CONNECT_TOKEN).
//
// Items created/managed by this helper are tagged "docker-credential-helpers"
// and store the registry's normalized URL as the first entry in the item's URLs.
// Username and password are stored in standard "login" fields.
//
// Optional environment variables:
//   - OP_VAULT: if set, passed to `op` to select a vault for create/edit ops.
//
// Runtime assumptions:
//   - The `op` CLI is installed and available in PATH.
//   - For Connect usage, OP_CONNECT_HOST and OP_CONNECT_TOKEN are set and valid.
package onepassword

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/docker/docker-credential-helpers/credentials"
	"github.com/docker/docker-credential-helpers/registryurl"
)

const (
	// Tag used to filter/list items created by this helper.
	opTag = "docker-credential-helpers"

	// Optional env to target a specific vault.
	envVault = "OP_VAULT"
)

type OnePassword struct{}

// We cache a quick initialization check (similar spirit to pass.Pass).
var (
	initMu      sync.Mutex
	initialized bool
)

// CheckInitialized is a cheap readiness probe you can call in tests.
func (h OnePassword) CheckInitialized() bool {
	return h.checkInitialized() == nil
}

func (h OnePassword) checkInitialized() error {
	initMu.Lock()
	defer initMu.Unlock()
	if initialized {
		return nil
	}
	// Minimal sanity check: is `op` present and responds?
	if _, err := h.runOP("--version"); err != nil {
		return fmt.Errorf("1Password CLI 'op' not available: %w", err)
	}
	initialized = true
	return nil
}

// --- credentials.Helper implementation ---

// Add appends or updates credentials for a given registry.
func (h OnePassword) Add(creds *credentials.Credentials) error {
	if creds == nil {
		return errors.New("missing credentials")
	}
	if ok, err := creds.IsValid(); !ok {
		return err
	}
	if err := h.checkInitialized(); err != nil {
		return err
	}

	u, err := registryurl.Parse(creds.ServerURL)
	if err != nil {
		return err
	}
	server := normalizedURL(u.String())

	// If an item for this server already exists, update it; otherwise create it.
	id, err := h.findItemByServer(server)
	if err != nil {
		return err
	}
	if id == "" {
		return h.createItem(server, creds.Username, creds.Secret)
	}
	return h.editItem(id, server, creds.Username, creds.Secret)
}

// Delete removes credentials for the given registry.
func (h OnePassword) Delete(serverURL string) error {
	if err := h.checkInitialized(); err != nil {
		return err
	}
	u, err := registryurl.Parse(serverURL)
	if err != nil {
		return err
	}
	server := normalizedURL(u.String())

	id, err := h.findItemByServer(server)
	if err != nil {
		return err
	}
	if id == "" {
		// Nothing to delete; keep behavior consistent with other helpers.
		return nil
	}
	// Delete without archiving.
	_, err = h.runOP("item", "delete", id, "--archive=false", "--force")
	return err
}

// Get retrieves username and secret for the given registry.
func (h OnePassword) Get(serverURL string) (string, string, error) {
	if err := h.checkInitialized(); err != nil {
		return "", "", err
	}
	u, err := registryurl.Parse(serverURL)
	if err != nil {
		return "", "", err
	}
	server := normalizedURL(u.String())

	id, err := h.findItemByServer(server)
	if err != nil {
		return "", "", err
	}
	if id == "" {
		return "", "", credentials.NewErrCredentialsNotFound()
	}

	user, err := h.getField(id, "username")
	if err != nil {
		return "", "", err
	}
	pass, err := h.getField(id, "password")
	if err != nil {
		return "", "", err
	}
	return user, pass, nil
}

// List returns serverURL->username for all items tagged for Docker.
func (h OnePassword) List() (map[string]string, error) {
	if err := h.checkInitialized(); err != nil {
		return nil, err
	}
	entries, err := h.listTagged()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		// Resolve details to obtain URL + username
		d, err := h.getItemJSON(e.ID)
		if err != nil {
			continue // skip malformed items
		}
		server := firstURL(d)
		if server == "" {
			continue
		}
		server = normalizedURL(server)
		user := fieldValue(d, "username")
		if user == "" {
			continue
		}
		out[server] = user
	}
	return out, nil
}

// --- Internals ---

// Item list entry returned by `op item list --tags <tag> --format json`
type opListEntry struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Tags  []string `json:"tags"`
}

// Generic "item get" structure (we only read a few fields)
type opItem struct {
	ID    string       `json:"id"`
	Title string       `json:"title"`
	URLs  []opItemURL  `json:"urls"`
	Fields []opItemField `json:"fields"`
}
type opItemURL struct {
	Href string `json:"href"`
}
type opItemField struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

func (h OnePassword) listTagged() ([]opListEntry, error) {
	b, err := h.runOP("item", "list", "--tags", opTag, "--format", "json")
	if err != nil {
		return nil, err
	}
	var out []opListEntry
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (h OnePassword) getItemJSON(id string) (*opItem, error) {
	b, err := h.runOP("item", "get", id, "--format", "json")
	if err != nil {
		return nil, err
	}
	var it opItem
	if err := json.Unmarshal(b, &it); err != nil {
		return nil, err
	}
	return &it, nil
}

// findItemByServer returns the item ID whose first URL matches `server`.
func (h OnePassword) findItemByServer(server string) (string, error) {
	entries, err := h.listTagged()
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		d, err := h.getItemJSON(e.ID)
		if err != nil {
			continue
		}
		u := firstURL(d)
		if normalizedURL(u) == server {
			return e.ID, nil
		}
	}
	return "", nil
}

func (h OnePassword) createItem(server, username, secret string) error {
	args := []string{
		"item", "create",
		"--category", "login",
		"--title", fmt.Sprintf("docker://%s", encodeServerKey(server)),
		"--url", server,
		"--tags", opTag,
		fmt.Sprintf("username=%s", username),
		fmt.Sprintf("password=%s", secret),
	}
	if v := os.Getenv(envVault); v != "" {
		args = append(args, "--vault", v)
	}
	_, err := h.runOP(args...)
	return err
}

func (h OnePassword) editItem(id, server, username, secret string) error {
	args := []string{
		"item", "edit", id,
		"--url", server,
		fmt.Sprintf("username=%s", username),
		fmt.Sprintf("password=%s", secret),
	}
	if v := os.Getenv(envVault); v != "" {
		args = append(args, "--vault", v)
	}
	_, err := h.runOP(args...)
	return err
}

func (h OnePassword) getField(id, field string) (string, error) {
	// Use op's field extraction for efficiency.
	b, err := h.runOP("item", "get", id, "--field", field)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (h OnePassword) runOP(args ...string) ([]byte, error) {
	cmd := exec.Command("op", args...) // #nosec G204
	// Pass-through environment; rely on user to set OP_CONNECT_* and auth.
	cmd.Env = os.Environ()
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("op %s: %s", strings.Join(args, " "), msg)
	}
	return out.Bytes(), nil
}

func firstURL(it *opItem) string {
	if it == nil || len(it.URLs) == 0 {
		return ""
	}
	return strings.TrimSpace(it.URLs[0].Href)
}

func fieldValue(it *opItem, label string) string {
	if it == nil {
		return ""
	}
	for _, f := range it.Fields {
		if strings.EqualFold(f.Label, label) {
			return f.Value
		}
	}
	return ""
}

func normalizedURL(s string) string {
	// Store as raw string but be strict about trimming. We already parsed in Add/Get/Delete.
	return strings.TrimSpace(s)
}

func encodeServerKey(server string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(server))
}