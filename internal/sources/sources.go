// Package sources is the deployment's choice of senders (docs/design.md NT01): the configuration's form and the
// streams and event types it subscribes to. Pure functions.
package sources

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	core "github.com/brickKit/infra-notification/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/notify"
)

var componentForm = regexp.MustCompile(`^[a-z][a-z0-9-]*/[a-z][a-z0-9-]*$`)

// Parse reads NOTIFICATION_INTENT_SOURCES: component IDs, comma separated; empty is none. A malformed entry or one
// given twice is an error (the member does not start). The answer is sorted.
func Parse(s string) ([]string, error) {
	out := []string{}
	if strings.TrimSpace(s) == "" {
		return out, nil
	}
	for _, c := range strings.Split(s, ",") {
		c = strings.TrimSpace(c)
		if !componentForm.MatchString(c) {
			return nil, fmt.Errorf("%s: %q is not a component ID", core.KeySources, c)
		}
		if slices.Contains(out, c) {
			return nil, fmt.Errorf("%s: %s is given twice", core.KeySources, c)
		}
		out = append(out, c)
	}
	slices.Sort(out)
	return out, nil
}

// Channels reads NOTIFICATION_CHANNELS (NT13): name=component, comma separated; empty is none. A malformed entry, a
// name or component given twice, or the name in_app is an error (the member does not start).
func Channels(s string) ([]core.Channel, error) {
	out := []core.Channel{}
	if strings.TrimSpace(s) == "" {
		return out, nil
	}
	for _, e := range strings.Split(s, ",") {
		name, comp, ok := strings.Cut(strings.TrimSpace(e), "=")
		name, comp = strings.TrimSpace(name), strings.TrimSpace(comp)
		switch {
		case !ok || !channelName.MatchString(name) || !componentForm.MatchString(comp):
			return nil, fmt.Errorf("%s: %q is not name=component", core.KeyChannels, e)
		case name == core.InApp:
			return nil, fmt.Errorf("%s: %s is the component's own channel", core.KeyChannels, name)
		}
		for _, c := range out {
			if c.Name == name || c.Component == comp {
				return nil, fmt.Errorf("%s: %s is given twice", core.KeyChannels, e)
			}
		}
		out = append(out, core.Channel{Name: name, Component: comp})
	}
	return out, nil
}

var channelName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// Stream is a sender's stream (be-protocol P18.2).
func Stream(component string) string { return notify.Stream(component) }

// Type is a sender's intent event type (P18.2).
func Type(component string) string { return notify.Type(component) }
