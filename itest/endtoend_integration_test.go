//go:build integration

package itest

// The notification component against PostgreSQL and the bus over its real surfaces (L2). The senders are the test
// itself: it publishes notification intents on the streams of test components (test/buy and test/pay are the
// deployment's senders, test/evil is not), as their forwarders would; no shared stream of a real component is
// touched. The positions expand against a directory the test sets (the employee projection needs a real
// mdm/employee). Needs BE_SDK_TEST_PG_ADMIN and BE_SDK_TEST_NATS_URL. The member listens on 28202 (user) and 9202
// (system). Run: go test -tags integration ./itest/ (make test-integration); the index of the steps is
// itest/AGENTS.md.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/brickKit/infra-notification/v3/internal/core"
	notification "github.com/brickKit/infra-notification/v3/notification"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/channelpb"
	"github.com/brickKit/be-sdk-go/v3/component"
	"github.com/brickKit/be-sdk-go/v3/config"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/events"
	"github.com/brickKit/be-sdk-go/v3/events/bus"
	"github.com/brickKit/be-sdk-go/v3/events/jetstream"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/launcher"
	"github.com/brickKit/be-sdk-go/v3/runtime"
	"github.com/brickKit/be-sdk-go/v3/sectest"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	itPort   = "28202"
	itBase   = "http://localhost:" + itPort
	itSystem = "localhost:9202"
	itChan   = "localhost:19207" // the fake channel member (integration/im-dingtalk)
	itSchema = "infra_notification_it"
	senders  = "test/buy,test/pay"                    // the deployment's choice (NT01); test/evil is not one
	carol    = "0190f3a1-0000-7000-8000-0000000000c3" // reads and marks her messages, sets her preferences
	dave     = "0190f3a1-0000-7000-8000-0000000000d4" // no grant at all
)

var streams = []events.Stream{{Name: "test.buy", Partitions: 4}, {Name: "test.pay", Partitions: 4}, {Name: "test.evil", Partitions: 4}}

// directory is the positions' holders the test sets (core.Directory), and whether the projection can prove each.
type directory struct {
	mu       sync.Mutex
	holders  map[string][]string // by legal entity/position
	complete map[string]bool
}

func (d *directory) set(pos string, complete bool, holders ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.holders[pos], d.complete[pos] = holders, complete
}

func (d *directory) Holders(_ context.Context, _ db.Tx, le, pos, _ string) ([]string, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.holders[le+"/"+pos], d.complete[le+"/"+pos], nil
}

// fixture is the running member and the world, shared by the steps.
type fixture struct {
	t     *testing.T
	ctx   context.Context
	w     *sectest.World
	admin *pgx.Conn
	tr    events.Transport
	dir   *directory
	chn   *fakeChannel
	alice client // everything, the refused intents too
	bob   client
	carol client
	dave  client
	le    string
}

func TestNotificationEndToEnd(t *testing.T) {
	fx := start(t)
	fx.delivery()
	fx.merging()
	fx.preferences()
	fx.positions()
	fx.expiry()
	fx.externals()
	fx.inbox()
	fx.senderCheck()
	fx.recoveryChecks()
}

func start(t *testing.T) *fixture {
	adminURL, natsURL := os.Getenv("BE_SDK_TEST_PG_ADMIN"), os.Getenv("BE_SDK_TEST_NATS_URL")
	if adminURL == "" || natsURL == "" {
		t.Fatal("BE_SDK_TEST_PG_ADMIN and BE_SDK_TEST_NATS_URL are needed")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminURL)
	require.NoError(t, err)
	t.Cleanup(func() { admin.Close(ctx) })
	_, err = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+itSchema+" CASCADE; CREATE SCHEMA "+itSchema)
	require.NoError(t, err)
	u, err := url.Parse(adminURL)
	require.NoError(t, err)
	pw, _ := u.User.Password()
	w := sectest.New(t)
	w.GrantAll(sectest.Alice, core.Definitions())
	for _, who := range []string{sectest.Bob, carol} { // every user reads and marks their own messages and preferences
		for _, op := range []struct{ res, op string }{{core.Messages, core.OpMessageRead}, {core.Messages, core.OpMessageMark},
			{core.Preferences, core.OpPrefRead}, {core.Preferences, core.OpPrefManage}} {
			w.Grant(who, sdkauthz.Segment{ResourceType: op.res, Operation: op.op, Atoms: map[string]sdkauthz.Atom{},
				Valid: sdkauthz.Window{From: "2026-01-01T00:00:00Z"}})
		}
	}
	fx := &fixture{t: t, ctx: ctx, w: w, admin: admin, le: ids.New().String(), dir: &directory{holders: map[string][]string{}, complete: map[string]bool{}}}
	portFree(t, itPort, itSystem, itChan)
	fx.chn = &fakeChannel{unmapped: map[string]bool{carol: true}}
	ln, err := net.Listen("tcp", itChan)
	require.NoError(t, err)
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(w.TLS(t, "integration/im-dingtalk").ServerConfig())))
	channelpb.RegisterChannelServiceServer(srv, fx.chn)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)

	// the test streams, clean each run (another run's events would come back)
	js, err := jetstream.Connect(natsURL, "notification-it-clean")
	require.NoError(t, err)
	for _, s := range streams {
		_ = js.DeleteStream(ctx, s.Name)
	}
	_ = js.Close()
	fx.tr, err = bus.Open(ctx, natsURL, "notification-it", slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { fx.tr.Close() })
	for _, s := range streams {
		require.NoError(t, fx.tr.EnsureStream(ctx, s))
	}

	env := w.Env(t, core.ID)
	for k, v := range map[string]string{"COMPONENT_ID": core.ID, "COMPONENT_VERSION": notification.Version, "PORT": itPort,
		"STOP_DRAIN_DELAY": "50ms", "PG_HOST": u.Hostname(), "PG_PORT": u.Port(), "PG_DATABASE": strings.TrimPrefix(u.Path, "/"),
		"PG_USER": u.User.Username(), "PG_PASSWORD": pw, "PG_SCHEMA": itSchema, "PG_OWNER_USER": u.User.Username(),
		"PG_OWNER_PASSWORD": pw, "BUS_URL": natsURL, "IMPLEMENTATION_INSTANCE": ids.New().String(),
		core.KeySources: senders, core.KeyChannels: "dingtalk=integration/im-dingtalk",
		"INTEGRATION_IM_DINGTALK_GRPC_ENDPOINT": "http://" + itChan} {
		env[k] = v
	}
	var logs bytes.Buffer
	var logMu sync.Mutex
	t.Cleanup(func() {
		if t.Failed() {
			logMu.Lock()
			t.Log(logs.String())
			logMu.Unlock()
		}
	})
	out := &lockedWriter{w: &logs, mu: &logMu}
	opts := launcher.Options{Stdout: out, Stderr: out,
		Env: config.Env{LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }, Read: os.ReadFile}}
	migrate := opts
	migrate.Args = []string{"migrate"}
	def := notification.Definition()
	newModule := def.New
	def.New = func(rt *runtime.Runtime) (component.Module, error) {
		m, err := newModule(rt)
		if err == nil {
			m.(*notification.Module).UseDirectory(fx.dir)
		}
		return m, err
	}
	require.Equal(t, launcher.ExitOK, launcher.Run(ctx, def, migrate))
	sctx, stop := context.WithCancel(ctx)
	done := make(chan int, 1)
	go func() { done <- launcher.Run(sctx, def, opts) }()
	t.Cleanup(func() { stop(); <-done })
	require.Eventually(t, func() bool {
		r, err := http.Get(itBase + "/readyz")
		if err == nil {
			r.Body.Close()
		}
		return err == nil && r.StatusCode == 200
	}, 20*time.Second, 100*time.Millisecond)
	fx.alice = client{t, w.Bearer(sectest.Alice)}
	fx.bob = client{t, w.Bearer(sectest.Bob)}
	fx.carol = client{t, w.Bearer(carol)}
	fx.dave = client{t, w.Bearer(dave)}
	return fx
}

// --- the senders' side ---

// intent is a deliver intent by the shared contract (be-protocol schemas/notification/intent.v1) to the recipients
// given, expiring in an hour.
func intent(from, category string, recipients ...any) map[string]any {
	now := time.Now().UTC()
	return map[string]any{"intent_id": ids.New().String(), "sender": from, "category": category, "kind": "deliver",
		"occurred_at": now.Format(time.RFC3339Nano), "recipients": recipients, "title": "Order D-1",
		"link": "/workflow/tasks/x", "expires_at": now.Add(time.Hour).Format(time.RFC3339Nano)}
}

func principal(p string) map[string]any { return map[string]any{"principal": p} }

// posKey is a position recipient's name in the deliveries: expanded on today's date (UTC).
func (fx *fixture) posKey(pos string) string {
	return "position:" + fx.le + "/" + pos + "@" + time.Now().UTC().Format(time.DateOnly)
}

func (fx *fixture) position(pos string) map[string]any {
	return map[string]any{"position": map[string]any{"legal_entity_id": fx.le, "position_id": pos}}
}

// withdraw is a withdraw intent of a merge key.
func withdraw(from, key string, seq int64) map[string]any {
	return map[string]any{"intent_id": ids.New().String(), "sender": from, "category": "approval_task", "kind": "withdraw",
		"occurred_at": time.Now().UTC().Format(time.RFC3339Nano), "collapse_key": key, "collapse_seq": seq}
}

// send publishes an intent on a test component's stream, as that component's forwarder would, and answers the
// event's ID.
func (fx *fixture) send(from string, in map[string]any) string {
	fx.t.Helper()
	id, _ := in["intent_id"].(string)
	if id == "" {
		id = ids.New().String()
	}
	return fx.publish(from, strings.ReplaceAll(from, "/", ".")+"_notification.intent.v1", id, 1, events.Full, in)
}

// processed waits until the component's subscription has processed the event (the SDK's inbox: it did not fail).
func (fx *fixture) processed(event string) {
	fx.t.Helper()
	require.Eventually(fx.t, func() bool {
		var n int
		err := fx.admin.QueryRow(fx.ctx, "SELECT count(*) FROM "+itSchema+".be_inbox WHERE event_id = $1", event).Scan(&n)
		return err == nil && n == 1
	}, 10*time.Second, 50*time.Millisecond, "event %s processed", event)
}

// taken waits until the component has taken the intent in (or refused it: then use refused).
func (fx *fixture) taken(from string, in map[string]any) {
	fx.t.Helper()
	require.Eventually(fx.t, func() bool {
		var n int
		err := fx.admin.QueryRow(fx.ctx, "SELECT count(*) FROM "+itSchema+".intent WHERE sender = $1 AND id = $2",
			from, in["intent_id"]).Scan(&n)
		return err == nil && n == 1
	}, 10*time.Second, 50*time.Millisecond, "%s's intent %v taken in", from, in["intent_id"])
}

// ended waits until the intent is no longer pending (delivered, or superseded) and answers its state.
func (fx *fixture) ended(from string, in map[string]any) string {
	fx.t.Helper()
	var state string
	require.Eventually(fx.t, func() bool {
		err := fx.admin.QueryRow(fx.ctx, "SELECT state FROM "+itSchema+".intent WHERE sender = $1 AND id = $2",
			from, in["intent_id"]).Scan(&state)
		return err == nil && state != "pending"
	}, 20*time.Second, 100*time.Millisecond, "%s's intent %v ended", from, in["intent_id"])
	return state
}

// results are an intent's in-app delivery results by recipient (external channels: externals).
func (fx *fixture) results(from string, in map[string]any) map[string]string {
	fx.t.Helper()
	rows, err := fx.admin.Query(fx.ctx, "SELECT recipient, result FROM "+itSchema+".delivery WHERE sender = $1 AND intent_id = $2"+
		" AND channel = 'in_app'", from, in["intent_id"])
	require.NoError(fx.t, err)
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var r, res string
		require.NoError(fx.t, rows.Scan(&r, &res))
		out[r] = res
	}
	return out
}

// messages are a reader's messages of one intent: [withdrawn] per message.
func (fx *fixture) messagesOf(c client, in map[string]any) []bool {
	fx.t.Helper()
	code, page := c.do("GET", "/api/notification/messages?limit=200", nil)
	require.Equal(fx.t, 200, code, "%v", page)
	var out []bool
	for _, x := range page["messages"].([]any) {
		m := x.(map[string]any)
		if m["sender"] == in["sender"] && m["title"] == in["title"] && m["link"] == in["link"] && m["id"] != nil &&
			fx.messageIntent(m["id"].(string)) == in["intent_id"] {
			out = append(out, m["withdrawn"].(bool))
		}
	}
	return out
}

func (fx *fixture) messageIntent(id string) string {
	var intent string
	_ = fx.admin.QueryRow(fx.ctx, "SELECT intent_id::text FROM "+itSchema+".message WHERE id = $1", id).Scan(&intent)
	return intent
}

// publish puts an event on a test component's stream, as that component's forwarder would (its service identity is
// the source).
func (fx *fixture) publish(from, typ, id string, version int64, kind events.Projection, data map[string]any) string {
	fx.t.Helper()
	return fx.publishAs(from, from, typ, id, version, kind, data)
}

// publishAs puts an event on one component's stream with another's name as its envelope source: what a publisher
// that forges its source would send (the bus only lets it publish on its own stream).
func (fx *fixture) publishAs(from, source, typ, id string, version int64, kind events.Projection, data map[string]any) string {
	fx.t.Helper()
	raw, err := json.Marshal(data)
	require.NoError(fx.t, err)
	stream := events.Stream{Name: strings.ReplaceAll(from, "/", "."), Partitions: 4}
	row := events.Row{EventID: ids.New().String(), Stream: stream.Name, Partition: stream.PartitionOf(id), Type: typ,
		OccurredAt: time.Now(), RecordID: id, Version: version, Projection: kind, Payload: raw, Seq: time.Now().UnixNano(),
		Epoch: events.Epoch{Generation: 1, Timeline: 1}}
	row.Digest = events.Digest(row.EventID, raw)
	require.NoError(fx.t, fx.tr.Publish(fx.ctx, events.Message{Subject: events.Subject(stream.Name, row.Partition, row.Type),
		Headers: row.Headers(source, strings.ReplaceAll(from, "/", "-")+"-it"), Data: raw}))
	return row.EventID
}

// --- helpers ---

type client struct {
	t     *testing.T
	token string
}

func (c client) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, itBase+path, r)
	req.Header.Set("Authorization", c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(c.t, err)
	defer resp.Body.Close()
	var out map[string]any
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// portFree fails at once when a port the member needs is taken (a running cell or another member would answer).
func portFree(t *testing.T, addrs ...string) {
	for _, a := range addrs {
		if !strings.Contains(a, ":") {
			a = "localhost:" + a
		}
		ln, err := net.Listen("tcp", a)
		require.NoError(t, err, fmt.Sprintf("port %s is taken: is a cell or another member running?", a))
		_ = ln.Close()
	}
}
