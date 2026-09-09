// Package repo 是 infra-notification 的数据访问层：通知记录、通道偏好、
// 收件人联系方式快照。三条铁律（设计计划 §6.6 同类判据）在这一层的体现
// 是"零表连业务库"——本组件只存自己的三张表 + 标准 Outbox/Inbox，没有
// 一处 join 或回查任何业务组件的 schema。
package repo

import (
	"database/sql"
	"errors"
	"fmt"
)

// ── 哨兵错误。grpc/http 两层通过 service.ToStatus 统一映射（同
// infra-workflow/erp-finance 的既有判据）。────────────────────────────────

var ErrNotFound = errors.New("not found")
var ErrInvalidArgument = errors.New("参数不合法")

type Repo struct {
	db     *sql.DB
	role   string
	schema string
}

func New(db *sql.DB, role, schema string) *Repo {
	return &Repo{db: db, role: role, schema: schema}
}

func wrap(action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", action, err)
}
