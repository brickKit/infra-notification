// Package preferences is the user surface of the preferences (docs/design.md NT07, NT09): my switch of every
// category and channel of the catalog, and changing one; a category that must be delivered cannot be changed.
package preferences

import (
	"context"
	"slices"

	core "github.com/brickKit/infra-notification/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/httpx"
)

// Preference is one category and channel of the catalog for the caller: on or off, and whether it may be changed.
type Preference struct {
	Category  string `json:"category"`
	Channel   string `json:"channel"`
	Enabled   bool   `json:"enabled"`
	Mandatory bool   `json:"mandatory"`
	Default   bool   `json:"default"`
}

// mine reads the caller's switches over the catalog: a row overrides the category's default.
func mine(ctx context.Context, tx db.Tx, me string) ([]Preference, error) {
	set := map[[2]string]bool{}
	rows, err := tx.Query(ctx, `SELECT category, channel, enabled FROM preference WHERE principal = $1`, me)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var cat, ch string
		var on bool
		if err := rows.Scan(&cat, &ch, &on); err != nil {
			rows.Close()
			return nil, err
		}
		set[[2]string{cat, ch}] = on
	}
	rows.Close()
	out := []Preference{}
	for _, c := range core.Catalog {
		for _, ch := range c.Channels {
			p := Preference{Category: c.Name, Channel: ch, Enabled: c.DefaultOn, Mandatory: c.Mandatory, Default: c.DefaultOn}
			if on, ok := set[[2]string{c.Name, ch}]; ok && !c.Mandatory {
				p.Enabled = on
			}
			out = append(out, p)
		}
	}
	return out, rows.Err()
}

// Mine answers the caller's preferences (NT09).
func Mine(m *core.Base, c *httpx.Context) error {
	ctx, me, err := core.Check(c, core.OpPrefRead)
	if err != nil {
		return err
	}
	var out []Preference
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		out, err = mine(ctx, tx, me)
		return err
	})
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"preferences": out})
}

// Set turns one category's channel on or off for the caller (NT09): a category or channel not in the catalog, or a
// category that must be delivered, answers REQUEST_INVALID.
func Set(m *core.Base, c *httpx.Context) error {
	ctx, me, err := core.Check(c, core.OpPrefManage)
	if err != nil {
		return err
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.BindJSON(&body); err != nil || body.Enabled == nil {
		return core.Invalid("enabled", "enabled is true or false")
	}
	cat, ok := core.CategoryOf(c.Param("category"))
	switch {
	case !ok:
		return core.Invalid("category", "not a category of the catalog")
	case !slices.Contains(cat.Channels, c.Param("channel")):
		return core.Invalid("channel", "not a channel of this category")
	case cat.Mandatory:
		return core.Invalid("category", "this category must be delivered: it cannot be turned off or on")
	}
	var out []Preference
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO preference (principal, category, channel, enabled) VALUES ($1, $2, $3, $4)
			ON CONFLICT (principal, category, channel) DO UPDATE SET enabled = excluded.enabled, updated_at = now()`,
			me, cat.Name, c.Param("channel"), *body.Enabled); err != nil {
			return err
		}
		var err error
		out, err = mine(ctx, tx, me)
		return err
	})
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"preferences": out})
}
