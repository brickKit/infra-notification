// Package inbox is the user surface of the in-app messages (docs/design.md NT08): my messages with the unread count,
// marking one or all of them read. Every query is bounded by "the recipient is me": another one's message answers 404.
package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	core "github.com/brickKit/infra-notification/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/jackc/pgx/v5"
)

var uuidForm = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Message is one in-app message as the user surface answers it.
type Message struct {
	ID        string            `json:"id"`
	Sender    string            `json:"sender"`
	Category  string            `json:"category"`
	Title     string            `json:"title"`
	Link      string            `json:"link"`
	Template  *string           `json:"template"`
	Params    map[string]string `json:"params"`
	CreatedAt string            `json:"created_at"`
	ReadAt    *string           `json:"read_at"`
	Withdrawn bool              `json:"withdrawn"`
}

const cols = `id::text, sender, category, title, link, template, params, created_at, read_at, withdrawn_at IS NOT NULL`

func scan(row interface{ Scan(...any) error }) (Message, error) {
	var m Message
	var params []byte
	var created time.Time
	var read *time.Time
	if err := row.Scan(&m.ID, &m.Sender, &m.Category, &m.Title, &m.Link, &m.Template, &params, &created, &read,
		&m.Withdrawn); err != nil {
		return m, err
	}
	if params != nil {
		_ = json.Unmarshal(params, &m.Params)
	}
	m.CreatedAt = created.UTC().Format(time.RFC3339Nano)
	if read != nil {
		s := read.UTC().Format(time.RFC3339Nano)
		m.ReadAt = &s
	}
	return m, nil
}

// Mine lists the caller's messages, the newest first, by cursor; unread=true lists only the unread ones (not read,
// not withdrawn). The answer carries the unread count.
func Mine(m *core.Base, c *httpx.Context) error {
	ctx, me, err := core.Check(c, core.OpMessageRead)
	if err != nil {
		return err
	}
	limit, err := core.Limit(c.Query("limit"))
	if err != nil {
		return err
	}
	after, err := core.FromCursor(c.Query("cursor"), 2)
	if err != nil {
		return err
	}
	unread := false
	switch c.Query("unread") {
	case "", "false":
	case "true":
		unread = true
	default:
		return core.Invalid("unread", "unread is true or false")
	}
	out := []Message{}
	var count int
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM message WHERE principal = $1 AND read_at IS NULL
			AND withdrawn_at IS NULL`, me).Scan(&count); err != nil {
			return err
		}
		q := `SELECT ` + cols + ` FROM message WHERE principal = $1`
		args := []any{me, limit + 1}
		if unread {
			q += ` AND read_at IS NULL AND withdrawn_at IS NULL`
		}
		if after != nil {
			q += ` AND (created_at, id) < ($3::timestamptz, $4::uuid)`
			args = append(args, after[0], after[1])
		}
		rows, err := tx.Query(ctx, q+` ORDER BY created_at DESC, id DESC LIMIT $2`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			msg, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, msg)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		next = core.Cursor(last.CreatedAt, last.ID)
	}
	return c.JSON(200, map[string]any{"messages": out, "unread": count, "next_cursor": next})
}

// MarkRead marks one of the caller's messages read (once: the first moment stays); another one's answers 404.
func MarkRead(m *core.Base, c *httpx.Context) error {
	ctx, me, err := core.Check(c, core.OpMessageMark)
	if err != nil {
		return err
	}
	id := c.Param("id")
	if !uuidForm.MatchString(id) {
		return core.NotFound("message")
	}
	var msg Message
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		row := tx.QueryRow(ctx, `UPDATE message SET read_at = coalesce(read_at, now()) WHERE id = $1::uuid
			AND principal = $2 RETURNING `+cols, id, me)
		var err error
		msg, err = scan(row)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return core.NotFound("message")
	}
	if err != nil {
		return err
	}
	return c.JSON(200, msg)
}

// MarkAllRead marks every unread message of the caller read, no one else's.
func MarkAllRead(m *core.Base, c *httpx.Context) error {
	ctx, me, err := core.Check(c, core.OpMessageMark)
	if err != nil {
		return err
	}
	var n int64
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE message SET read_at = now() WHERE principal = $1 AND read_at IS NULL
			AND withdrawn_at IS NULL`, me)
		n = tag.RowsAffected()
		return err
	})
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"marked": n})
}
