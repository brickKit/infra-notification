-- notification_records：每条通知一行（设计计划 §2）。⚠️ 本组件唯一会
-- 无限增长的表——一条待办可能触发多条通知（多通道 × 多次提醒），量级
-- 是待办的几倍，所以按月分区、且归档窗口全系统最短（3 个月，设计计划
-- §7），这也是它出现在总纲 §11.2.5 而 infra-workflow 不在的原因。
--
-- 状态机：PENDING → ACCEPTED（⭐ 已受理待确认，不是已送达，见下方 ⚠️）
-- → SENT / FAILED_PERMANENT（终态）；SUPPRESSED 是被用户偏好挡掉的终态；
-- RETRYING 是 ACCEPTED 之后收到失败结果、等待重试的活跃态。
--
-- ⚠️ 从 PENDING 直接跳 SENT 是错的——查证钉钉文档后确认 asyncsend_v2
-- 只返回"钉钉受理了"的 task_id，真实投递结果要再调 getsendresult 查
-- （integration-im-dingtalk 设计计划 §4.2）。ACCEPTED 这个中间态是初版
-- 设计漏掉、查完钉钉文档才补上的，不能再省。
CREATE TABLE notification_records (
    id                BIGSERIAL,
    recipient_sub     TEXT        NOT NULL,
    category          TEXT        NOT NULL,               -- 如 "workflow_task"
    channel           TEXT        NOT NULL,                -- IM/EMAIL/SMS
    title             TEXT        NOT NULL DEFAULT '',
    body              TEXT        NOT NULL,                -- 渲染好的文本，原样透传给适配器
    status            TEXT        NOT NULL DEFAULT 'PENDING'
                                   CHECK (status IN ('PENDING', 'ACCEPTED', 'SENT', 'FAILED_PERMANENT', 'SUPPRESSED', 'RETRYING')),
    external_task_id  TEXT        NOT NULL DEFAULT '',     -- 适配器返回的任务 id（如钉钉 task_id）
    -- 来源四元组：这条通知是哪个业务组件的哪个事件触发的。
    source_component  TEXT        NOT NULL,
    source_aggregate  TEXT        NOT NULL,
    source_id         TEXT        NOT NULL,
    retry_count       INT         NOT NULL DEFAULT 0,
    last_error        TEXT        NOT NULL DEFAULT '',
    -- §11.2.1 强制字段。
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    version           BIGINT      NOT NULL DEFAULT 1,
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
-- 数据权限一维：owner=recipient_sub（等值，设计计划 §1）。
CREATE INDEX notification_records_recipient_idx ON notification_records (recipient_sub, created_at);
CREATE INDEX notification_records_status_idx ON notification_records (status, created_at)
  WHERE status IN ('PENDING', 'ACCEPTED', 'RETRYING');
CREATE INDEX notification_records_source_idx ON notification_records (source_component, source_aggregate, source_id);

-- ⚠️ 初始分区覆盖当前月起 3 个月（迁移执行时是 2026-09）。其余分区由
-- 组件内置定时任务自动建（决策 54、§11.5.1，backend/internal/partition/
-- monthly.go）。分区名必须是 "表名_YYYY_MM_01"，同 erp-inventory 的既有
-- 判据——两处生成分区名的代码路径必须共用同一套格式。
CREATE TABLE notification_records_2026_09_01 PARTITION OF notification_records
  FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');
CREATE TABLE notification_records_2026_10_01 PARTITION OF notification_records
  FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');
CREATE TABLE notification_records_2026_11_01 PARTITION OF notification_records
  FOR VALUES FROM ('2026-11-01') TO ('2026-12-01');
-- 建分区要求执行者是父表 owner，迁移用管理凭据跑，建出来的分区默认
-- 属于那个账号；分区维护后台任务运行时用 infra_notification_rw
-- （SET LOCAL ROLE 切换）建未来的分区，两者不是同一身份，必须显式把
-- owner 转过去（同 erp-inventory 的既有教训）。
ALTER TABLE notification_records OWNER TO infra_notification_rw;

-- notification_preferences：两层模型（设计计划 §2.1）——category = ''
-- 这一行是"全局通道开关"，具体分类名（如 'workflow_task'）是"分类通道
-- 开关"。⚠️ category 是"层"的判据本身，不是普通的筛选字段：
-- resolvePreferredChannels 的解析逻辑靠它区分该读哪一层。
CREATE TABLE notification_preferences (
    id         BIGSERIAL   PRIMARY KEY,
    sub        TEXT        NOT NULL,
    category   TEXT        NOT NULL DEFAULT '',  -- '' = 全局；具体分类名 = 分类层
    channels   TEXT[]      NOT NULL,             -- 这一层启用的通道，如 {IM}
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX notification_preferences_sub_category_uniq
  ON notification_preferences (sub, category);

-- user_contacts：infra-iam-casdoor 事件的快照（sub -> 手机号/邮箱/显示名），
-- 权威源在 Casdoor，本组件只持快照（同 erp-inventory 持 tracking_type
-- 快照的既有先例，设计计划 §2）。永不归档——人停用后仍要能显示"发给过谁"。
CREATE TABLE user_contacts (
    sub          TEXT        PRIMARY KEY,
    phone        TEXT        NOT NULL DEFAULT '',
    email        TEXT        NOT NULL DEFAULT '',
    display_name TEXT        NOT NULL DEFAULT '',
    disabled     BOOLEAN     NOT NULL DEFAULT false,  -- user.disabled.v1 必须消费（设计计划 §4 ⚠️）
    version      BIGINT      NOT NULL DEFAULT 0,      -- 按 version 单调比较，旧事件丢弃
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
