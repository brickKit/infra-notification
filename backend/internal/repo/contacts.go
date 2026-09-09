package repo

import (
	"context"
	"database/sql"
)

// Contact 是 user_contacts 的一行——infra-iam-casdoor 事件的快照（权威源
// 在 Casdoor，本组件只持快照，设计计划 §2）。
type Contact struct {
	Sub         string
	Phone       string
	Email       string
	DisplayName string
	Disabled    bool
	Version     int64
}

// GetContactTx 供事件消费路径使用（同一事务内）。查不到时返回零值
// Contact（Phone/Email 为空、Disabled 为 false），不报错——一个还没同步
// 到 user_contacts 快照的用户（比如 iam 事件乱序，disabled 比 created
// 先到；或者本组件是后装的，历史用户从未触发过 created 事件）不该让消费
// 事件的整条链路失败，调用方（consumer）看到空手机号会按"待决问题 2"
// 的既定方向转 FAILED_PERMANENT，而不是这里报错。
func GetContactTx(ctx context.Context, tx *sql.Tx, sub string) (Contact, error) {
	var c Contact
	c.Sub = sub
	err := tx.QueryRowContext(ctx,
		`SELECT phone, email, display_name, disabled, version FROM user_contacts WHERE sub = $1`, sub,
	).Scan(&c.Phone, &c.Email, &c.DisplayName, &c.Disabled, &c.Version)
	if err == sql.ErrNoRows {
		return c, nil
	}
	return c, err
}

// UpsertContactTx 维护 created/updated 两个事件的快照——按 version
// 单调比较，旧的丢弃（同 erp-finance UpsertCustomerCreditSnapshotTx 的
// 既有判据：ON CONFLICT DO UPDATE ... WHERE 表.version < EXCLUDED.version）。
// ⚠️ 不touch disabled 列：一次资料更新不该把一个已停用的用户悄悄重新
// 启用——disabled 只由 SetContactDisabledTx 管，两条写入路径职责分离。
func UpsertContactTx(ctx context.Context, tx *sql.Tx, sub, displayName, email, phone string, version int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO user_contacts (sub, display_name, email, phone, version)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (sub) DO UPDATE
		   SET display_name = EXCLUDED.display_name, email = EXCLUDED.email,
		       phone = EXCLUDED.phone, version = EXCLUDED.version, updated_at = now()
		 WHERE user_contacts.version < EXCLUDED.version`,
		sub, displayName, email, phone, version)
	return err
}

// SetContactDisabledTx 消费 infra.iam.user.disabled.v1——设计计划 §4
// ⚠️ 明文要求必须消费：人走了还继续发审批通知，是本组件最容易出的、
// 外部可见的错。只置 disabled，不动其余字段（同 UpsertContactTx 反向
// 的职责分离）。行不存在时也要能记下"这个人被停用了"，所以用 UPSERT
// 而不是纯 UPDATE。
func SetContactDisabledTx(ctx context.Context, tx *sql.Tx, sub string, version int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO user_contacts (sub, disabled, version)
		VALUES ($1, true, $2)
		ON CONFLICT (sub) DO UPDATE
		   SET disabled = true, version = EXCLUDED.version, updated_at = now()
		 WHERE user_contacts.version < EXCLUDED.version`,
		sub, version)
	return err
}
