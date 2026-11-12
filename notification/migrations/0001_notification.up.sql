-- The notification component (docs/design.md, the data): the intents taken in, the largest sequence of each merge
-- key, the in-app messages, the preferences, the deliveries and the refused intents.
CREATE TABLE intent (
    sender       text NOT NULL,
    id           uuid NOT NULL,
    category     text NOT NULL,
    kind         text NOT NULL CHECK (kind IN ('deliver', 'withdraw')),
    recipients   jsonb NOT NULL DEFAULT '[]',
    title        text,
    link         text,
    template     text,
    params       jsonb,
    collapse_key text,
    collapse_seq bigint,
    occurred_at  timestamptz NOT NULL,
    expires_at   timestamptz,
    received_at  timestamptz NOT NULL DEFAULT now(),
    state        text NOT NULL CHECK (state IN ('pending', 'done', 'superseded')),
    ended_at     timestamptz,
    PRIMARY KEY (sender, id)
);
CREATE INDEX intent_pending ON intent (received_at, sender, id) WHERE state = 'pending';
CREATE INDEX intent_collapse ON intent (sender, collapse_key) WHERE state = 'pending' AND collapse_key IS NOT NULL;
CREATE INDEX intent_ended ON intent (ended_at) WHERE state <> 'pending';

-- NT04: the merge key's largest sequence, kept longer than any intent or message
CREATE TABLE collapse (
    sender text NOT NULL,
    key    text NOT NULL,
    seq    bigint NOT NULL,
    at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (sender, key)
);

CREATE TABLE message (
    id           uuid PRIMARY KEY,
    principal    text NOT NULL,
    sender       text NOT NULL,
    intent_id    uuid NOT NULL,
    category     text NOT NULL,
    title        text NOT NULL,
    link         text NOT NULL,
    template     text,
    params       jsonb,
    collapse_key text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    read_at      timestamptz,
    withdrawn_at timestamptz
);
CREATE INDEX message_mine ON message (principal, created_at DESC, id DESC);
CREATE INDEX message_unread ON message (principal) WHERE read_at IS NULL AND withdrawn_at IS NULL;
CREATE INDEX message_collapse ON message (sender, collapse_key) WHERE collapse_key IS NOT NULL AND withdrawn_at IS NULL;
CREATE INDEX message_created ON message (created_at);

CREATE TABLE preference (
    principal  text NOT NULL,
    category   text NOT NULL,
    channel    text NOT NULL,
    enabled    boolean NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (principal, category, channel)
);

-- NT05: one delivery per intent, principal and channel; a position that could not be expanded in time is recorded
-- under its position (recipient 'position:<legal entity>/<position>')
CREATE TABLE delivery (
    sender    text NOT NULL,
    intent_id uuid NOT NULL,
    recipient text NOT NULL,
    channel   text NOT NULL,
    result    text NOT NULL CHECK (result IN ('written', 'preference_off', 'expired', 'unresolvable')),
    at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (sender, intent_id, recipient, channel),
    FOREIGN KEY (sender, intent_id) REFERENCES intent (sender, id) ON DELETE CASCADE
);

CREATE TABLE rejected_intent (
    id        uuid PRIMARY KEY,
    sender    text NOT NULL,
    intent_id text NOT NULL,
    reason    text NOT NULL,
    at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX rejected_intent_at ON rejected_intent (at DESC, id DESC);
