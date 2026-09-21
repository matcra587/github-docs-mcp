package mcpserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/matcra587/github-docs-mcp/internal/docs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	cursorVersion   = 1
	maxCursorBytes  = 16 * 1024
	pageWindowBytes = 50 * 1024
)

type selection struct {
	Slug    string `json:"slug,omitempty"`
	Heading string `json:"heading,omitempty"`
	Query   string `json:"query,omitempty"`
	Section string `json:"section,omitempty"`
}

type continuation struct {
	Version   int       `json:"version"`
	Tool      string    `json:"tool"`
	Origin    string    `json:"origin"`
	Selection selection `json:"selection"`
	Position  int       `json:"position"`
	Offset    int       `json:"offset"`
	Limit     int       `json:"limit"`
	Snapshot  string    `json:"snapshot"`
}

func encodeCursor(cursor continuation) (string, error) {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}

	encoded := base64.RawURLEncoding.EncodeToString(raw)
	if len(encoded) > maxCursorBytes {
		return "", errors.New("selection is too large for a continuation cursor; use a shorter slug, heading, query or section")
	}

	return encoded, nil
}

func decodeCursor(encoded, tool, origin string) (continuation, error) {
	var cursor continuation
	if encoded == "" || len(encoded) > maxCursorBytes {
		return cursor, errors.New("invalid or oversized cursor; restart the original call")
	}

	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return cursor, errors.New("malformed cursor; restart the original call")
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&cursor); err != nil {
		return cursor, errors.New("malformed cursor; restart the original call")
	}

	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return cursor, errors.New("malformed cursor; restart the original call")
	}

	if cursor.Version != cursorVersion {
		return cursor, errors.New("unsupported cursor version; restart the original call")
	}

	if cursor.Tool != tool || cursor.Origin != origin {
		return cursor, errors.New("cursor belongs to another tool or origin; restart the original call")
	}

	if err := cursor.validate(); err != nil {
		return cursor, err
	}

	return cursor, nil
}

func (c continuation) validate() error {
	fingerprint, err := hex.DecodeString(c.Snapshot)
	if err != nil || len(fingerprint) != sha256.Size || c.Position < 0 || c.Offset < 0 {
		return errors.New("invalid cursor snapshot or position; restart the original call")
	}

	switch c.Tool {
	case toolListDocs:
		return c.validateList()
	case toolGetDoc:
		return c.validatePage()
	default:
		return errors.New("invalid cursor tool")
	}
}

func (c continuation) validateList() error {
	if c.Limit < 1 || c.Limit > listLimitMax || c.Offset != 0 || c.Position == 0 {
		return errors.New("invalid catalogue cursor limit or position")
	}

	if c.Selection.Slug != "" || c.Selection.Heading != "" || c.Selection.Query != "" {
		return errors.New("invalid catalogue cursor selection")
	}

	return nil
}

func (c continuation) validatePage() error {
	if c.Selection.Slug == "" || c.Selection.Section != "" {
		return errors.New("invalid page cursor selection")
	}

	if c.Selection.Query != "" && c.Selection.Heading == "" {
		if c.Limit != sectionLimit || c.Position%sectionLimit != 0 {
			return errors.New("invalid section cursor limit or position")
		}
	} else if c.Limit != pageWindowBytes || c.Position != 0 {
		return errors.New("invalid page cursor limit or position")
	}

	if c.Position == 0 && c.Offset == 0 {
		return errors.New("invalid cursor position; restart the original call")
	}

	return nil
}

func resolveCursor(req *mcp.CallToolRequest, encoded string, initial continuation) (continuation, bool, error) {
	resuming, err := cursorOnly(req, encoded)
	if err != nil {
		return initial, resuming, err
	}

	if !resuming {
		return initial, false, nil
	}

	cursor, err := decodeCursor(encoded, initial.Tool, initial.Origin)

	return cursor, true, err
}

// cursorOnly checks presence, including explicitly supplied zero/empty fields.
func cursorOnly(req *mcp.CallToolRequest, value string) (bool, error) {
	if req == nil || req.Params == nil || len(req.Params.Arguments) == 0 {
		return value != "", nil
	}

	var args map[string]json.RawMessage
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return false, fmt.Errorf("invalid arguments: %w", err)
	}

	_, present := args["cursor"]
	if present && len(args) != 1 {
		return true, errors.New("cursor must be the only argument; omit selection, limit and offset")
	}

	return present, nil
}

func pageFingerprint(page docs.Page) string {
	source := page.Source
	identity, _ := json.Marshal([]string{source.URL, source.Language, source.Product, source.Version})
	hash := sha256.New()
	_, _ = hash.Write(identity)
	_, _ = hash.Write(page.Content)

	return hex.EncodeToString(hash.Sum(nil))
}

func catalogueFingerprint(entries []docs.Doc) string {
	raw, _ := json.Marshal(entries)
	hash := sha256.Sum256(raw)

	return hex.EncodeToString(hash[:])
}

func (c continuation) restartError() *mcp.CallToolResult {
	args := map[string]any{}
	if c.Tool == toolListDocs {
		args["limit"] = c.Limit
		if c.Selection.Section != "" {
			args["section"] = c.Selection.Section
		}
	} else {
		args["slug"] = c.Selection.Slug
		if c.Selection.Heading != "" {
			args["heading"] = c.Selection.Heading
		}

		if c.Selection.Query != "" {
			args["query"] = c.Selection.Query
		}
	}

	raw, _ := json.Marshal(args)

	return errorResult(fmt.Sprintf("snapshot changed; restart with %s(%s)", c.Tool, raw))
}

func continuationText(cursor continuation) (string, error) {
	encoded, err := encodeCursor(cursor)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("\nContinue: %s({\"cursor\":\"%s\"})\n", cursor.Tool, encoded), nil
}
