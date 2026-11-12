// Command seed fills a running infra/notification with the seed data for people (be-assembly-standard's seed and
// test data conventions). The notification component owns no data of its own to seed: its messages come from the
// senders' intents (the approvals erp/purchase's and erp/payment's seeds leave waiting reach their approvers as
// in-app messages through the flow kernel). It grants, by the authorization admin:
//
//   - the role maintain to the seed user: their own messages and preferences, and the refused intents;
//   - the role inbox to the approvers of the seeds, test.buyer.lead and test.finance.lead, and to the buyers and
//     cashier who submit and follow: their own messages and preferences.
//
// A second run changes nothing. It waits until the grants reach the component (each account's messages answer).
//
//	SEED_IAM_URL (http://localhost:18200)    the identity member's development exchange
//	SEED_AUTHZ_URL (http://localhost:18223)  the authorization member's effect point
//	SEED_ADMIN (test.alice)                  the authorization admin (the install claim)
//	SEED_USER (test.bob), SEED_PASSWORD      the seed user; the test accounts' password
//	INFRA_NOTIFICATION_URL (http://localhost:18202)
package main

import (
	"fmt"
	"os"
	"time"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/seed"
)

const prefix = "「本地测试」"

// users are the accounts that get an inbox: the approvers and the ones who submit and follow.
var users = []string{"test.buyer.lead", "test.finance.lead", "test.buyer.cn", "test.cashier.cn"}

func main() {
	base := env("INFRA_NOTIFICATION_URL", "http://localhost:18202")
	if !seed.Ready(base) {
		fail("infra/notification at %s is not ready", base)
	}
	pw := os.Getenv("SEED_PASSWORD")
	if pw == "" {
		fail("SEED_PASSWORD is not set (the test accounts' password in Casdoor)")
	}
	iam, user, admin := env("SEED_IAM_URL", "http://localhost:18200"), env("SEED_USER", "test.bob"), env("SEED_ADMIN", "test.alice")
	accounts, err := seed.Accounts(iam, pw, append([]string{admin, user}, users...)...)
	check(err)
	a, err := seed.NewAuthz(env("SEED_AUTHZ_URL", "http://localhost:18223"), accounts[admin].Token, "infra/notification")
	check(err)
	inbox := []sdkauthz.Segment{
		segment("infra.notification.message", "infra.notification.message.read"),
		segment("infra.notification.message", "infra.notification.message.mark"),
		segment("infra.notification.preference", "infra.notification.preference.read"),
		segment("infra.notification.preference", "infra.notification.preference.manage"),
	}
	_, err = a.GrantRoles([]seed.RoleSpec{
		{Key: "maintain", Name: prefix + "通知维护", To: []string{user},
			Segments: append(inbox, segment("infra.notification.rejection", "infra.notification.rejection.read"))},
		{Key: "inbox", Name: prefix + "我的消息", To: users, Segments: inbox},
	}, accounts)
	check(err)
	// the messages are read only and the operation is checked first: without the grant the list answers 404, so a
	// 200 is the grant arrived (UntilGranted on a read, the SDK's seed package)
	for _, u := range append([]string{user}, users...) {
		code, out, err := seed.NewClient(base, accounts[u].Token).UntilGranted(time.Minute, "GET", "/api/notification/messages", nil)
		check(err)
		if code != 200 {
			fail("%s's messages: %d %v", u, code, out)
		}
	}
	fmt.Printf("seed infra/notification: the inbox granted to %d accounts\n", len(users)+1)
}

// segment grants an operation on a resource of the notification component (no dimension: one's own messages).
func segment(resource, op string) sdkauthz.Segment {
	s := seed.Segment(resource, op)
	s.Atoms = map[string]sdkauthz.Atom{}
	return s
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "seed infra/notification: "+format+"\n", a...)
	os.Exit(1)
}

func check(err error) {
	if err != nil {
		fail("%v", err)
	}
}
