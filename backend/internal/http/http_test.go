package http

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	_ "github.com/jackc/pgx/v5/stdlib"

	besdk "github.com/brickKit/be-sdk-go"
	"github.com/brickKit/infra-notification/v2/backend/internal/repo"
	"github.com/brickKit/infra-notification/v2/backend/internal/service"
)

// 这里测的是每条 REST 路由绑的是哪个权限键：请求走真实的 besdk 权限
// 中间件——本地起一个 JWKS 服务器和一个 bundle 服务器，用测试自己生成的
// RSA 私钥签真 RS256 token。只有 view 的角色与同时有 view、edit 的角色
// 各一个，核对"能读不能写"。

const (
	testKID    = "notification-http-test"
	roleViewer = "notification_viewer"
	roleEditor = "notification_editor"
)

var (
	authzOnce sync.Once
	signKey   *rsa.PrivateKey
)

// setupAuthz 给整个测试进程装一份权限判定（SDK 的判定状态是进程级的，
// 只能装一次），并等 bundle 第一次拉取完成——之前的请求一律 503。
func setupAuthz(t *testing.T) {
	t.Helper()
	authzOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		signKey = key
		b64 := base64.RawURLEncoding.EncodeToString
		jwks, _ := json.Marshal(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": testKID, "use": "sig", "alg": "RS256",
			"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
		jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(jwks)
		}))
		bundle, _ := json.Marshal(map[string]any{
			"roles": map[string][]string{
				roleViewer: {"infra.notification.view"},
				roleEditor: {"infra.notification.view", "infra.notification.preference.edit"},
			},
			"stale_since": map[string]int64{},
		})
		bundleSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(bundle)
		}))
		besdk.InitShellAuthz(context.Background(), besdk.NewConfig(map[string]string{
			"IAM_JWKS_URL": jwksSrv.URL, "AUTHZ_BUNDLE_URL": bundleSrv.URL,
		}), slog.Default())
	})

	probe := gin.New()
	besdk.GET(probe, "/probe", "infra.notification.view", func(c *gin.Context) { c.Status(http.StatusOK) })
	deadline := time.Now().Add(10 * time.Second)
	for {
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		req.Header.Set("Authorization", "Bearer "+token(t, "probe", roleViewer))
		w := httptest.NewRecorder()
		probe.ServeHTTP(w, req)
		if w.Code == http.StatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("10 秒内权限判定没有就绪：探针仍返回 %d（%s）", w.Code, w.Body.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func token(t *testing.T, sub, role string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub": sub, "iat": time.Now().Add(-time.Minute).Unix(), "roles": []string{role},
	})
	tok.Header["kid"] = testKID
	s, err := tok.SignedString(signKey)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func testService(t *testing.T) *service.Service {
	t.Helper()
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("未设置 TEST_PG_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return service.New(repo.New(db, "infra_notification_rw", "infra_notification"), slog.Default())
}

func serve(t *testing.T, eng *gin.Engine, method, path, body, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, req)
	return w
}

// TestRoutes_只有view的用户能读偏好不能写：读自己的偏好与读自己的通知
// 历史是同一档权限（infra.notification.view），偏好页面打开就要读一次，
// 不应要求编辑权限；改偏好仍要 infra.notification.preference.edit。排障
// 视图要 infra.notification.admin，view 与 edit 都打不开。
func TestRoutes_只有view的用户能读偏好不能写(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupAuthz(t)
	eng := gin.New()
	RegisterRoutes(eng, testService(t))

	sub := fmt.Sprintf("http-perm-%d", time.Now().UnixNano())
	viewer := token(t, sub, roleViewer)
	editor := token(t, sub, roleEditor)
	put := `{"global_channels":["IM"]}`

	cases := []struct {
		name, method, path, body, bearer string
		want                             int
	}{
		{"view 读通知历史", http.MethodGet, "/infra/notification/notifications", "", viewer, http.StatusOK},
		{"view 读偏好", http.MethodGet, "/infra/notification/preferences", "", viewer, http.StatusOK},
		{"view 改偏好", http.MethodPut, "/infra/notification/preferences", put, viewer, http.StatusForbidden},
		{"view 看排障视图", http.MethodGet, "/infra/notification/admin/records", "", viewer, http.StatusForbidden},
		{"edit 改偏好", http.MethodPut, "/infra/notification/preferences", put, editor, http.StatusOK},
		{"edit 看排障视图", http.MethodGet, "/infra/notification/admin/records", "", editor, http.StatusForbidden},
	}
	for _, tc := range cases {
		w := serve(t, eng, tc.method, tc.path, tc.body, tc.bearer)
		if w.Code != tc.want {
			t.Errorf("%s：%s %s 期望 %d，实际 %d（%s）", tc.name, tc.method, tc.path, tc.want, w.Code, w.Body.String())
		}
	}
}
