-- NT13, NT14: the external channels. An intent keeps its action grants and the channels that take their actions;
-- its delivery result event (NT10) is published once its external deliveries all have results (published_at).
-- An external delivery waits as pending with its delivery id (the channel member's idempotency) and when it is next
-- tried; it ends handed_over, unmapped, failed, expired or superseded.
ALTER TABLE intent ADD COLUMN actions jsonb;
ALTER TABLE intent ADD COLUMN action_channels text[];
ALTER TABLE intent ADD COLUMN published_at timestamptz;
UPDATE intent SET published_at = ended_at WHERE state = 'done';

ALTER TABLE delivery ADD COLUMN delivery_id uuid;
ALTER TABLE delivery ADD COLUMN next_at timestamptz;
ALTER TABLE delivery ADD COLUMN attempts int NOT NULL DEFAULT 0;
ALTER TABLE delivery DROP CONSTRAINT delivery_result_check;
ALTER TABLE delivery ADD CONSTRAINT delivery_result_check
    CHECK (result IN ('written', 'preference_off', 'expired', 'unresolvable', 'expanded', 'pending', 'handed_over',
                      'unmapped', 'failed', 'superseded'));
CREATE INDEX delivery_external_due ON delivery (next_at) WHERE result = 'pending';
