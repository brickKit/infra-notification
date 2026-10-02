[English](design.md) · [中文](design.zh.md)

# infra/notification design

Conclusions only, for whoever changes this component's design. How to use it: `BRICKKIT.md`; how the code is laid out: `AGENTS.md`.

## Boundaries

This component translates intents into messages: a business event says "this person has a task", and this component decides how many messages that becomes and on which channels. It owns the users' channel preferences, a snapshot of recipients' contact details, the delivery state and retries of every message, and the notification history. Retry logic is written once, here; external platform keys live nowhere near it.

| Not here | Owner | Why |
|---|---|---|
| The call to DingTalk, email or SMS | `integration/*` channel adapters | Platform keys, SDKs and rate limits travel with the adapter; a notification centre holding every platform's secrets is the problem this split avoids |
| When something deserves a notification | the component raising the event (today `infra/workflow`) | That is a business rule; this component acts only once it is told |
| The wording of the business situation | the raising component, in its event payload | This component builds the message text from the payload (today `您有一条新的待办事项待处理：<title>`), it does not decide what a rejection should say |
| Who a user is, the authoritative phone and email | `infra/iam-casdoor` (Casdoor behind it) | This component keeps a snapshot from identity events |
| Whether a platform error is worth retrying | the adapter | Only the adapter recognises platform error codes ("not a member of this organisation" is permanent) |
| Showing the notification list | the frontend | This component serves the REST reads |

Each consumed source event adds a small piece of "take the recipient from the payload" and nothing more: no "if the amount exceeds X also notify the manager". When the number of consumed source events grows past five, the step to take is a generic intent event (`infra.notification.requested.v1`, the raiser supplying recipient and template), not a sixth handler; before that, a generic event would force every raiser to change for no gain.

Delivery strategy: every resolved channel is sent in parallel, and the user can switch channels off. "Fall back to SMS only when IM fails" or "one primary channel per user" are other reasonable strategies; they differ only in a few dozen lines of routing inside this component and share the same events, so they are a fork point of this component, not a slot family of separate components.

## Owned data

| Table | Partitioned | Notes |
|---|---|---|
| `notification_records` | monthly, by `created_at` | One row per message: recipient, category, channel, title, body, status, `external_task_id`, `retry_count`, `last_error`, source component / aggregate / ID. The only table that grows without bound: one task can become several messages (channels × reminders) |
| `notification_preferences` | no | `(sub, category)` unique; `category = ''` is the global layer, a category name the category layer; `channels` is a `text[]`. Bounded by the number of users |
| `user_contacts` | no | `sub` → phone, email, display name, `disabled`, `version`. The identity snapshot, bounded by the number of users; kept after a user is disabled so history can still say who a message went to |
| `event_outbox`, `event_inbox` | weekly, by `created_at` | The outbox the pump publishes from, the inbox that deduplicates consumed events |

Statuses: `PENDING` (record created, dispatch not yet confirmed by an adapter) → `ACCEPTED` (the platform accepted the message; not yet delivered) → `SENT`, or `RETRYING` → … → `FAILED_PERMANENT`. `SUPPRESSED` is a separate terminal state for a message nobody may receive (today: the recipient is disabled). Terminal: `SENT`, `FAILED_PERMANENT`, `SUPPRESSED`.

`ACCEPTED` cannot be skipped: DingTalk's work-notification API returns a task ID meaning "accepted", and the real outcome must be queried later. Jumping from `PENDING` to `SENT` would record "delivered" where only "submitted" is known, the hardest kind of complaint to investigate.

A recipient without a phone number gets a record in `FAILED_PERMANENT` with the reason, not `SUPPRESSED`: `SUPPRESSED` means "kept back by preference or by the user's state", a missing phone is a data gap. The record stays visible in the troubleshooting view.

Preferences, two layers, borrowed from Novu:

- The **global** layer is a ceiling ("no SMS for me"); never set means the default `[IM]`. EMAIL and SMS are not in the default because no adapter consumes them yet: switched on by default they would only create records that stay `PENDING` forever.
- The **category** layer narrows it ("approvals by IM, digests by email"); never set means "follow the global layer", set means the intersection with the global layer. A category cannot break through a global "off".
- **Critical categories** cannot be switched off: the user chooses the channel, not whether. Today `workflow_task` is critical, because an approval notification someone could mute breaks the business flow while everything looks fine. `PUT /preferences` rejects an empty list for a critical category (`400`); resolving a critical category that comes out empty falls back to the default.
- A **disabled user** receives nothing, critical or not. This is a separate check in the consumer, after the preference resolution, never merged into the critical fallback.
- `PUT /preferences` replaces the whole set. A field left out or sent as `null` means "not submitted": the global layer then follows the default, a category follows the global layer. An empty list means "this layer is off". (The `channels` column is `NOT NULL`; a missing value is never written as `NULL`.)

## Contract surface

gRPC `infra.notification.v1.NotificationService`, for other components:

| rpc | Kind | Notes |
|---|---|---|
| `BatchGetRecords` | read | the batched read; unknown or malformed IDs are left out |
| `ListRecords` | read | cursor paging (the last record ID), filters `category`, `status`; no user scoping: callers are components, and gRPC carries no user token |
| `GetPreferences` | read | by `sub`, defaults filled in |
| `SetPreferences` | write | by `sub`, whole replacement, idempotent by nature (no `idempotency_key`); an empty global list over gRPC means "global off" |

REST under `/infra/notification`, every route behind a key:

| Route | Permission key | Notes |
|---|---|---|
| `GET /notifications` | `infra.notification.view` | the caller's own history; `recipient_sub` forced to the caller |
| `GET /preferences` | `infra.notification.view` | the caller's own preferences. Reading them is the same level as reading one's own notifications: the preference page reads them on open, and a read must not require the edit key |
| `PUT /preferences` | `infra.notification.preference.edit` | replace the caller's preferences (rules above) |
| `GET /admin/records` | `infra.notification.admin` | troubleshooting: any recipient, optional `recipient_sub`; for example records stuck in `ACCEPTED` (`status=ACCEPTED`) mean an adapter's result query stopped |

No `Notify` rpc and no REST "send": the only way in is an event. This component is installed by default but can be left out, and a synchronous call would force every caller to depend on it, while a notification that cannot be sent must never make a business action fail; and `infra/workflow`, today's only source, must not call back synchronously into components that react to it. One way in also means one idempotency scheme and one retry path.

No status endpoint for timed-out writes and no `command_idempotency` table: the only writes are `SetPreferences` (idempotent by nature) and record creation, which is driven by events and deduplicated by the inbox.

## Events

Published, all through the outbox in the same transaction as the record change:

| Subject | When | Payload |
|---|---|---|
| `infra.notification.dispatch.im.v1` | an IM record is created, and on each retry | `record_id`, `attempt`, `target_adapters`, `recipient_phone`, `title`, `body` |
| `infra.notification.dispatch.email.v1` | an EMAIL record is created | `record_id`, `attempt`, `target_adapters`, `recipient_email`, `title`, `body` (no adapter consumes it yet) |
| `infra.notification.sent.v1` | an adapter confirms success | `record_id`, `channel` |
| `infra.notification.failed.v1` | an adapter reports a permanent failure, or retries are exhausted | `record_id`, `channel`, `error` |

- **One subject per channel family, addressed by `target_adapters`.** Every IM adapter (DingTalk, WeCom, Feishu, …) subscribes to `dispatch.im.v1`; NATS core delivers it to all of them, and each handles only dispatches that name it. Adding an IM adapter changes `IM_TARGET_ADAPTERS`, not this component; one subject per adapter would put install-time knowledge into the runtime.
- **The dispatch version is the attempt.** The aggregate is the `record_id`, so the first send and every retry share subject and aggregate; the adapter's inbox accepts only increasing versions, so `Version = attempt` (1, then 2, 3, 4). A resend with the old version is dropped silently: `retry_count` rises and nothing arrives.
- **Retries**: on a `CONFIRMED` failure with `retryable = true`, `retry_count` goes up and the dispatch is sent again at once (no back-off until real rate-limit data asks for one); after the third retry the record becomes `FAILED_PERMANENT`. `retryable = false` ends it at once.

Consumed:

| Subject | From | What it does | Duplicates and order |
|---|---|---|---|
| `infra.workflow.task.created.v1` | `infra/workflow` | the assignee's channels are resolved; one record per channel; a dispatch per IM / EMAIL record; a disabled assignee gets one `SUPPRESSED` record | inbox: the task ID and version |
| `infra.iam.user.created.v1`, `infra.iam.user.updated.v1` | `infra/iam-casdoor` | upsert phone, email, display name; never touches `disabled`, so a profile update cannot silently re-enable someone | applied only when the event version is greater than the stored one |
| `infra.iam.user.disabled.v1` | `infra/iam-casdoor` | sets `disabled`, creating the row if needed (the disable may arrive before the create) | same version rule |
| `integration.im.result.v1` | any IM adapter | `ACCEPTED` stores the adapter's task ID; `CONFIRMED` moves to `SENT`, `RETRYING` (and resends) or `FAILED_PERMANENT`; a late result for a finished record is ignored | inbox: per `record_id`, the adapter's version `attempt × 10 + phase` |

The result subject is one per family, with the adapter's name in the payload, for the same reason as the dispatch: a second IM adapter needs no change here. Consuming `user.disabled.v1` is mandatory: sending approvals to someone who has left is this component's most visible failure.

Consumption uses plain NATS subscriptions: events published while this component is down are not delivered afterwards. Durable delivery is a platform decision (JetStream in the SDK), not one this component takes alone.

## Dependencies

None, by design. Expected but absent:

| Not a dependency | Why |
|---|---|
| `infra/iam-casdoor` | contacts come from its events; a synchronous edge to the identity slot would defeat replacing it with another IAM |
| `integration/*` adapters | reached through dispatch events; channel adapters are optional, and a call would make them mandatory |
| `infra/workflow` | its events are consumed; the direction is one-way and it does not know this component exists |
| `infra/authz` | the permission bundle is polled from `AUTHZ_BUNDLE_URL`: configuration, not an edge |
| NATS | a base resource reached through `NATS_URL`, not a component |

Nobody depends on this component either: installed, notifications are routed; not installed, every business action still succeeds and nobody is told.

## Place in the synchronous call graph

Not in it: no outgoing and no incoming synchronous edge, so it can never close a cycle and has nothing to do with "no synchronous edge between CRM and ERP". It lives on the event graph only. Its shape is closest to `erp/finance` (an event sink that calls no one); the difference is that finance books what it consumes, this component sends it on.

## Partitioning and archiving

| Data | Hot | Archived when | Where |
|---|---|---|---|
| `notification_records` | the last 3 months | whole months older than that, and only rows in a terminal state | `infra_notification_archive` |
| `notification_preferences` | always | never | — |
| `user_contacts` | always | never (history must still name the recipient) | — |
| `event_outbox`, `event_inbox` | weekly partitions | with the outbox / inbox convention | — |

The window is the shortest in the project (3 months against 12): notifications are process data whose value drops faster than business documents', and they grow fastest. A `RETRYING` row is never archived because it is old. The component creates the monthly partitions itself, the current month plus three ahead, and the weekly ones four weeks ahead; partition names must match the ones the migrations created (`<table>_YYYY_MM_01`), otherwise the loop tries to create an overlapping range. Archiving itself is not implemented yet (see Open questions).

## Data scopes

One dimension: `owner` on `notification_records.recipient_sub`, `equals`. Notification history is visible to its recipient only, because a notification carries document titles and amounts: seeing someone else's would bypass the data scopes of the component that raised it. The REST list forces `recipient_sub` to the caller's `sub`, whatever the query says; there is no "manager sees the team's notifications". `GET /admin/records` bypasses the scope by design and needs `infra.notification.admin`. gRPC is component-to-component and unscoped.

## Reference implementations

| Project | Version | Module consulted | What was borrowed | License | Usage |
|---|---|---|---|---|---|
| Novu | not recorded | workflow / step model, subscriber and channel preferences | user-level channel preferences, reduced to `sub × category × channel`; global over category; critical notifications the user cannot switch off | MIT | Borrowed reasoning |
| Novu | not recorded | provider layer | the counter-example: providers as internal plugins; here they are separate components because the keys travel with the adapter | MIT | Borrowed reasoning |
| ERPNext | not recorded | `Notification` doctype, `Email Queue` | delivery state machine and retry count stored per message | GPL-3 | Borrowed reasoning |
| DingTalk open platform | — | work notifications | accepted versus delivered (task ID, then a result query); strict rate limits, one more reason to keep retries in one place | proprietary | Borrowed practice |

**Deliberately avoided**: a notification centre that holds every platform's keys and SDKs; business components calling a synchronous notify interface; notification conditions as configurable expressions (ERPNext's `condition`): a runtime string an AI cannot read or test, and "when to notify" belongs to the raiser. Not consulted on purpose: message-broker retry and dead-letter designs; that is transport-level retry (the SDK's outbox / inbox), while this component's retry is business-level ("not in the organisation" must not be retried).

## Open questions

| Question | Current answer |
|---|---|
| Archiving of `notification_records` after 3 months | Designed (terminal rows of whole months), not implemented; the monthly partitions keep accumulating until it is |
| Retry parameters | Three immediate retries; a back-off curve waits for real rate-limit data from an adapter. Errors an adapter cannot classify count as retryable, within the limit |
| A recipient without a phone (for example registered through social login) | `FAILED_PERMANENT` with the reason, visible in `GET /admin/records`; showing "could not notify" on the task itself would need `infra/workflow` to read it, not planned |
| Records stuck in `ACCEPTED` | Findable with `GET /admin/records?status=ACCEPTED`; no age threshold or alert yet |
| Read / unread in the notification list | Not now; an unread badge would add `GET /notifications/unread-count` and `POST /notifications/{id}/read` (key `infra.notification.view`, a `read_at` column) when the frontend asks for it |
| System alerts through this component | Not now; their recipients are operators, not business users, and the preference model may not fit them |
| EMAIL and SMS | Subjects reserved (`dispatch.email.v1`); retries and the result event exist for IM only, extended per channel when an adapter exists |
