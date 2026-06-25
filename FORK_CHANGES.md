# Fork Changes — Runtime Tag Filter Rules

This document describes all changes made to the original Mailpit codebase in this fork,
their purpose, usage instructions, and known limitations.

---

## Summary of Changes

| File | Type | Description |
|------|------|-------------|
| `internal/storage/tagfilters_settings.go` | **NEW** | Database persistence and bulk-apply for runtime tag filter rules |
| `server/apiv1/tagfilters.go` | **NEW** | REST API endpoints for tag filter CRUD and apply-to-existing |
| `internal/storage/tagfilters.go` | **MODIFIED** | `LoadTagFilters()` merges config-file rules with runtime (DB) rules |
| `server/server.go` | **MODIFIED** | Three new API routes registered |
| `server/ui-src/components/AppSettings.vue` | **MODIFIED** | New "Tag filters" tab in the Settings modal |
| `Dockerfile` | **MODIFIED** | Uses `-mod=vendor` + `vendor/` directory for offline/reliable builds |
| `vendor/` | **NEW** | Vendored Go dependencies (enables offline Docker builds) |

---

## Feature Description

### What it does

Mailpit already supported tag filter rules defined via CLI flags or a YAML config file.
This fork adds a **runtime GUI** for managing the same rules, stored in the database,
without requiring a restart or access to the server filesystem.

Rules are evaluated against all **new incoming messages** as they arrive via SMTP.
An explicit **Apply to existing messages** action allows retroactively tagging the inbox.

### How tags are applied

1. On every new inbound message, `tagFilterMatches(id)` runs all loaded filters against it.
2. Loaded filters = config-file rules + runtime (DB) rules, merged by `LoadTagFilters()`.
3. Any matching tags are added to the message automatically.
4. Config-file rules and GUI rules coexist — neither overrides the other.

---

## New Files

### `internal/storage/tagfilters_settings.go`

Adds three public functions:

| Function | Description |
|----------|-------------|
| `GetRuntimeTagFilters() []TagFilterRule` | Reads rules from the `settings` table (key `TagFilters`). Returns empty slice if none saved. |
| `SetRuntimeTagFilters(rules []TagFilterRule) ([]TagFilterRule, error)` | Validates, deduplicates, persists rules, then calls `LoadTagFilters()` to make them active immediately. |
| `ApplyTagFiltersToAll() (int, error)` | Iterates all existing messages, applies current filters additively (never removes existing tags), broadcasts a WebSocket `update` event per modified message so the UI refreshes live. Returns count of updated messages. |

**Data type:**
```go
type TagFilterRule struct {
    Match string   `json:"match"` // Mailpit search syntax
    Tags  []string `json:"tags"`  // one or more tag names
}
```

---

### `server/apiv1/tagfilters.go`

Three HTTP handlers:

| Handler | Method | Route | Description |
|---------|--------|-------|-------------|
| `GetTagFilters` | `GET` | `/api/v1/tag-filters` | Returns current runtime rules as JSON array |
| `SetTagFilters` | `PUT` | `/api/v1/tag-filters` | Accepts `{"Filters": [...]}`, saves and activates rules |
| `ApplyTagFilters` | `POST` | `/api/v1/tag-filters/apply` | Applies rules to all existing messages, returns `{"updated": N}` |

**PUT request body example:**
```json
{
  "Filters": [
    { "match": "subject:invoice", "tags": ["Fattura", "Finance"] },
    { "match": "has:attachment", "tags": ["Allegato"] },
    { "match": "from:github.com", "tags": ["GitHub"] }
  ]
}
```

---

## Modified Files

### `internal/storage/tagfilters.go`

**Change:** `LoadTagFilters()` now merges both sources before building SQL matchers.

Before:
```go
for _, t := range config.TagFilters {
    // only config-file rules
}
```

After:
```go
allFilters := make([]TagFilterRule, 0, ...)
for _, t := range config.TagFilters { allFilters = append(...) }
for _, t := range GetRuntimeTagFilters() { allFilters = append(...) }
// process merged list
```

This is backward-compatible — existing config/CLI tag rules continue to work unchanged.

---

### `server/server.go`

Three routes added in `apiRoutes()`:

```go
r.HandleFunc("GET "  + config.Webroot + "api/v1/tag-filters",       middleWareFunc(apiv1.GetTagFilters))
r.HandleFunc("PUT "  + config.Webroot + "api/v1/tag-filters",       middleWareFunc(apiv1.SetTagFilters))
r.HandleFunc("POST " + config.Webroot + "api/v1/tag-filters/apply", middleWareFunc(apiv1.ApplyTagFilters))
```

---

### `server/ui-src/components/AppSettings.vue`

A new **"Tag filters"** tab is added to the Settings modal.

**New reactive data properties:**

| Property | Type | Purpose |
|----------|------|---------|
| `tagFilters` | `Array` | List of `{match, tags}` objects for the UI |
| `tagFiltersLoading` | `Boolean` | Loading indicator while fetching rules |
| `tagFiltersSaving` | `Boolean` | Saving indicator while PUT is in flight |
| `tagFiltersLoaded` | `Boolean` | Lazy-load flag — rules are only fetched on first tab click |
| `tagFiltersApplying` | `Boolean` | Applying indicator while POST is in flight |
| `tagFiltersApplyResult` | `Number\|null` | Number of messages updated after apply, shown as feedback |

**New methods:**

| Method | Description |
|--------|-------------|
| `loadTagFilters()` | GET `/api/v1/tag-filters`, transforms tags array → comma-separated string for display |
| `addTagFilter()` | Appends an empty rule row |
| `removeTagFilter(index)` | Removes rule at index; ensures at least one empty row remains |
| `saveTagFilters()` | Transforms UI state back to API format, PUT to save & activate |
| `applyTagFilters()` | POST to apply current rules to all existing messages |

---

### `Dockerfile`

**Change:** `go build` now uses `-mod=vendor` to read dependencies from the local `vendor/`
directory instead of downloading them at build time.

Before:
```dockerfile
RUN apk upgrade && apk add git npm && \
    npm ci && npm run package && \
    CGO_ENABLED=0 go build -ldflags "..." -o /mailpit
```

After:
```dockerfile
RUN apk upgrade && apk add npm && \
    npm ci && npm run package && \
    CGO_ENABLED=0 go build -mod=vendor -ldflags "..." -o /mailpit
```

`git` is no longer needed at build time since modules are vendored.
The `vendor/` directory must be present in the repository root before running `docker build`.

To regenerate `vendor/` (requires Go installed locally):
```bash
go mod vendor
```

---

## How to Use the GUI

### Opening the settings

1. Open Mailpit in the browser (default: `http://127.0.0.1:8025`)
2. Click the **Settings** icon (⚙️) in the top-right corner
3. Click the **"Tag filters"** tab

### Creating rules

Each rule has two fields:

| Field | Description | Example |
|-------|-------------|---------|
| **Match** | A Mailpit search expression | `subject:invoice` |
| **Tags** | Comma-separated tag names to apply | `Fattura, Finance` |

Click **"Add rule"** to add rows. Click **"Remove rule"** to delete one.

### Saving rules

Click **"Save rules"**. Rules are saved to the database and immediately active
for all **new** messages arriving via SMTP.

### Applying rules to existing messages

Click **"Apply to existing messages"**. The operation:

- Scans every message already in the database
- Adds matching tags (never removes existing tags)
- Updates the UI live via WebSocket — no manual page refresh needed
- Shows `✓ N messages updated` on completion

---

## Search Syntax for Match Field

The `match` field supports the full Mailpit search syntax:

| Pattern | Matches |
|---------|---------|
| `subject:invoice` | Subject contains "invoice" |
| `from:github.com` | Sender domain is github.com |
| `to:ops@example.com` | Recipient is ops@example.com |
| `has:attachment` | Message has one or more attachments |
| `has:inline` | Message has inline images |
| `is:unread` | Unread messages |
| `larger:50kb` | Messages over 50 KB |
| `"exact phrase"` | Body/subject contains exact phrase |
| `-subject:spam` | Subject does NOT contain "spam" |
| `subject:alert from:monitoring` | Both conditions must match |

---

## Known Limitations

1. **Rules apply only to new messages by default.**
   Messages already in the database when a rule is created are not tagged automatically.
   Use **"Apply to existing messages"** to backfill.

2. **No rule ordering / priority.**
   All matching rules are applied — there is no "stop on first match" or priority system.
   A message can receive tags from multiple rules simultaneously.

3. **Tags are additive only.**
   `ApplyTagFiltersToAll()` never removes existing tags from messages — it only adds.
   `SetMessageTags()` (manual tag editing) continues to support full replace.

4. **Rules are not re-evaluated on tag rename/delete.**
   If you rename or delete a tag that is referenced in a rule, the rule still contains
   the old tag name. You must manually update the rule.

5. **No rule testing / dry-run.**
   There is no preview of how many messages a rule would match before saving.

6. **Config-file rules are read-only from the GUI.**
   Rules defined via CLI (`--tag`) or YAML config are shown only on the backend.
   They cannot be viewed, edited, or deleted from the Settings modal.

7. **`vendor/` directory required for Docker builds.**
   The modified Dockerfile uses `-mod=vendor`. If `vendor/` is missing, the Docker build
   will fail at the `go build` step. Regenerate it with `go mod vendor`.

8. **`ApplyTagFiltersToAll` is synchronous.**
   For large inboxes (tens of thousands of messages) the HTTP request may take several
   seconds to return. There is no progress indicator beyond the "Applying..." button state.

9. **No persistence of `tagFiltersApplying` state on error.**
   If the POST to `/api/v1/tag-filters/apply` fails, the button resets after a 10-second
   timeout (safety valve). No error message is shown to the user in the UI.

---

## Running with a Persistent Database

By default, the Docker container stores its database in memory (lost on restart).
To persist data across container restarts, mount a volume:

```bash
docker run -d \
  --name mailpit \
  -v mailpit-data:/data \
  -e MP_DATA_FILE=/data/mailpit.db \
  -p 8025:8025 \
  -p 1025:1025 \
  mailpit-local:dev
```

---

## Building the Docker Image

```bash
# 1. Regenerate vendor directory (once, or after go.mod changes)
go mod vendor

# 2. Build the image
docker build -t mailpit-local:dev .

# 3. Run
docker run -d --name mailpit-local-dev -p 8025:8025 -p 1025:1025 mailpit-local:dev
```
