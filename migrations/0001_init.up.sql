-- All timestamps are timestamptz. Message and update payloads must be redacted by ingestion.
CREATE TABLE chats (
    chat_id bigint PRIMARY KEY,
    chat_type text NOT NULL DEFAULT '',
    username text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    features jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE users (
    user_id bigint PRIMARY KEY,
    username text NOT NULL DEFAULT '',
    is_bot boolean,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE chat_user_stats (
    chat_id bigint NOT NULL REFERENCES chats(chat_id),
    user_id bigint NOT NULL REFERENCES users(user_id),
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    first_observed_message_id bigint,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    history_coverage text NOT NULL DEFAULT 'partial' CHECK (history_coverage IN ('partial','unknown')),
    messages_count bigint NOT NULL DEFAULT 0 CHECK (messages_count >= 0),
    active_days integer NOT NULL DEFAULT 0,
    admin_replies_30d integer NOT NULL DEFAULT 0,
    distinct_repliers_30d integer NOT NULL DEFAULT 0,
    trust integer NOT NULL DEFAULT 0 CHECK (trust BETWEEN 0 AND 100),
    trust_version text NOT NULL DEFAULT '',
    PRIMARY KEY(chat_id,user_id)
);
CREATE TABLE message_journal (
    chat_id bigint NOT NULL REFERENCES chats(chat_id),
    message_id bigint NOT NULL,
    user_id bigint REFERENCES users(user_id),
    sender_chat_id bigint,
    message_thread_id bigint,
    reply_to bigint,
    content text NOT NULL DEFAULT '',
    payload jsonb NOT NULL,
    entities jsonb NOT NULL DEFAULT '[]',
    urls jsonb NOT NULL DEFAULT '[]',
    edit_version integer NOT NULL DEFAULT 1 CHECK (edit_version > 0),
    stats_counted boolean NOT NULL DEFAULT false,
    deletion_state text NOT NULL DEFAULT 'unknown' CHECK (deletion_state IN ('unknown','verified_deleted')),
    occurred_at timestamptz NOT NULL,
    edited_at timestamptz,
    retention_until timestamptz,
    hold_until timestamptz,
    hold_reason text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(chat_id,message_id)
);
CREATE INDEX message_journal_recent ON message_journal(chat_id,occurred_at DESC,message_id DESC);
CREATE INDEX message_journal_author ON message_journal(chat_id,user_id,occurred_at DESC);
CREATE INDEX message_journal_retention ON message_journal(occurred_at);
CREATE TABLE message_revisions (
    chat_id bigint NOT NULL,
    message_id bigint NOT NULL,
    edit_version integer NOT NULL CHECK (edit_version > 0),
    payload jsonb NOT NULL,
    urls jsonb NOT NULL DEFAULT '[]',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(chat_id,message_id,edit_version),
    FOREIGN KEY(chat_id,message_id) REFERENCES message_journal(chat_id,message_id) ON DELETE CASCADE
);
CREATE TABLE message_urls (
    chat_id bigint NOT NULL,
    message_id bigint NOT NULL,
    ordinal integer NOT NULL,
    url text NOT NULL,
    domain text NOT NULL DEFAULT '',
    payload jsonb NOT NULL,
    PRIMARY KEY(chat_id,message_id,ordinal),
    FOREIGN KEY(chat_id,message_id) REFERENCES message_journal(chat_id,message_id) ON DELETE CASCADE
);
CREATE INDEX message_urls_domain ON message_urls(domain,chat_id);
CREATE TABLE ruleset_versions (
    version text PRIMARY KEY,
    rules jsonb NOT NULL DEFAULT '{}',
    model text NOT NULL DEFAULT '',
    prompt_version text NOT NULL DEFAULT '',
    source text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
-- Assessments/reviews are historical records, not overwritten on message edits.
CREATE TABLE risk_assessments (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chat_id bigint NOT NULL,
    message_id bigint NOT NULL,
    edit_version integer NOT NULL DEFAULT 1,
    ruleset_version text NOT NULL,
    score integer NOT NULL CHECK (score BETWEEN 0 AND 100),
    trust integer NOT NULL CHECK (trust BETWEEN 0 AND 100),
    mandatory boolean NOT NULL,
    review_required boolean NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(chat_id,message_id,edit_version,ruleset_version)
);
CREATE INDEX risk_assessments_message ON risk_assessments(chat_id,message_id,created_at DESC);
CREATE TABLE ai_jobs (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chat_id bigint NOT NULL,
    message_id bigint NOT NULL,
    kind text NOT NULL DEFAULT 'review',
    priority text NOT NULL CHECK (priority IN ('mandatory','optional')),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','done','failed')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz,
    lease_token bigint NOT NULL DEFAULT 0,
    payload jsonb NOT NULL,
    result jsonb,
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (priority <> 'mandatory' OR status <> 'failed')
);
-- Processing must participate in dedup too: a competing enqueue cannot bypass an active lease.
-- Use a revision-specific kind (e.g. review:2) when a new edit needs another paid review.
CREATE UNIQUE INDEX ai_jobs_idempotency ON ai_jobs(chat_id,message_id,kind)
    WHERE status IN ('pending','processing','done');
CREATE INDEX ai_jobs_claim ON ai_jobs(priority,available_at,id) WHERE status = 'pending';
CREATE INDEX ai_jobs_expired ON ai_jobs(lease_until) WHERE status = 'processing';
CREATE INDEX ai_jobs_budget ON ai_jobs(created_at) WHERE priority = 'optional';
CREATE TABLE ai_reviews (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    job_id bigint NOT NULL UNIQUE REFERENCES ai_jobs(id),
    chat_id bigint NOT NULL,
    message_id bigint NOT NULL,
    payload jsonb NOT NULL,
    provider text NOT NULL DEFAULT '',
    model text NOT NULL DEFAULT '',
    input_tokens bigint,
    output_tokens bigint,
    estimated_cost numeric,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE incidents (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chat_id bigint NOT NULL REFERENCES chats(chat_id),
    user_id bigint REFERENCES users(user_id),
    fingerprint text NOT NULL,
    window_start timestamptz NOT NULL,
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','needs_review','closed')),
    payload jsonb NOT NULL DEFAULT '{}',
    retain_until timestamptz NOT NULL DEFAULT (now() + interval '30 days'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
-- user_id may be unknown for anonymous sender_chat messages; NULL must still deduplicate.
CREATE UNIQUE INDEX incidents_dedup ON incidents(chat_id,COALESCE(user_id,0),fingerprint,window_start);
CREATE INDEX incidents_active ON incidents(chat_id,user_id,created_at DESC) WHERE status <> 'closed';
CREATE TABLE incident_messages (
    incident_id bigint NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    chat_id bigint NOT NULL,
    message_id bigint NOT NULL,
    evidence jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(incident_id,chat_id,message_id)
);
CREATE INDEX incident_messages_message ON incident_messages(chat_id,message_id);
CREATE TABLE alerts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    incident_id bigint NOT NULL REFERENCES incidents(id),
    destination_chat_id bigint NOT NULL,
    telegram_message_id bigint,
    idempotency_key text NOT NULL UNIQUE,
    payload jsonb NOT NULL DEFAULT '{}',
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','sent','unknown','failed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX alerts_incident ON alerts(incident_id);
CREATE TABLE outbox (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    idempotency_key text NOT NULL UNIQUE,
    kind text NOT NULL DEFAULT 'alert',
    payload jsonb NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','done','failed')),
    attempts integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz,
    lease_token bigint NOT NULL DEFAULT 0,
    last_error text NOT NULL DEFAULT '',
    result jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX outbox_claim ON outbox(available_at,id) WHERE status = 'pending';
CREATE INDEX outbox_expired ON outbox(lease_until) WHERE status = 'processing';
CREATE TABLE user_reports (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chat_id bigint NOT NULL REFERENCES chats(chat_id),
    reporter_id bigint NOT NULL REFERENCES users(user_id),
    target_message_id bigint NOT NULL,
    reported_user_id bigint,
    explanation text NOT NULL DEFAULT '',
    payload jsonb NOT NULL DEFAULT '{}',
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','reviewed','dismissed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(chat_id,reporter_id,target_message_id)
);
CREATE INDEX user_reports_target ON user_reports(chat_id,target_message_id);
CREATE TABLE moderation_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source text NOT NULL CHECK (source IN ('platform_chat_member','mtproto_admin_log','trusted_bot_webhook','admin_attested','unverified_text')),
    source_event_id text NOT NULL,
    chat_id bigint NOT NULL,
    target_user_id bigint NOT NULL,
    actor_user_id bigint,
    action text NOT NULL CHECK (action IN ('ban','kick','restrict','unrestrict','warn','delete','unknown')),
    occurred_at timestamptz NOT NULL,
    evidence_level text NOT NULL CHECK (evidence_level IN ('verified','attested','unverified')),
    related_message_id bigint,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(source,source_event_id,chat_id),
    CHECK (source <> 'unverified_text' OR evidence_level = 'unverified'),
    CHECK (source <> 'admin_attested' OR evidence_level = 'attested')
);
CREATE INDEX moderation_events_target ON moderation_events(chat_id,target_user_id,occurred_at DESC);
CREATE TABLE feedback_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    idempotency_key text NOT NULL UNIQUE,
    incident_id bigint NOT NULL REFERENCES incidents(id),
    admin_user_id bigint NOT NULL,
    label text NOT NULL,
    source text NOT NULL DEFAULT 'admin_callback',
    ruleset_version text NOT NULL DEFAULT '',
    payload jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX feedback_events_incident ON feedback_events(incident_id,created_at);
CREATE TABLE processed_updates (
    bot_instance_id text NOT NULL,
    update_id bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(bot_instance_id,update_id)
);
CREATE TABLE chat_cursor (
    bot_instance_id text PRIMARY KEY,
    last_update_id bigint NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE inbox (
    bot_instance_id text NOT NULL,
    id bigint NOT NULL,
    payload jsonb NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','done')),
    attempts integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz,
    lease_token bigint NOT NULL DEFAULT 0,
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(bot_instance_id,id),
    FOREIGN KEY(bot_instance_id,id) REFERENCES processed_updates(bot_instance_id,update_id)
);
CREATE INDEX inbox_claim ON inbox(bot_instance_id,available_at,id) WHERE status = 'pending';
CREATE INDEX inbox_expired ON inbox(lease_until) WHERE status = 'processing';
CREATE INDEX inbox_order ON inbox(bot_instance_id,created_at,id) WHERE status <> 'done';
CREATE TABLE daily_activity (
    chat_id bigint NOT NULL,
    user_id bigint NOT NULL,
    day date NOT NULL,
    messages_count bigint NOT NULL DEFAULT 0,
    admin_replies integer NOT NULL DEFAULT 0,
    admin_ids bigint[] NOT NULL DEFAULT '{}',
    replier_ids bigint[] NOT NULL DEFAULT '{}',
    PRIMARY KEY(chat_id,user_id,day),
    FOREIGN KEY(chat_id,user_id) REFERENCES chat_user_stats(chat_id,user_id)
);
CREATE INDEX daily_activity_cleanup ON daily_activity(day);
CREATE TABLE ai_attempts (
 job_id bigint NOT NULL REFERENCES ai_jobs(id) ON DELETE CASCADE,
 attempt integer NOT NULL,
 started_at timestamptz NOT NULL DEFAULT now(),
 finished_at timestamptz,
 status text NOT NULL DEFAULT 'started',
 error_code text NOT NULL DEFAULT '',
 PRIMARY KEY(job_id,attempt)
);
CREATE TABLE ai_budget(day date PRIMARY KEY, requests integer NOT NULL DEFAULT 0);
CREATE TABLE authorization_audit (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 user_id bigint NOT NULL,
 update_id bigint NOT NULL,
 allowed boolean NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE runtime_metrics(name text PRIMARY KEY, value bigint NOT NULL DEFAULT 0);
CREATE TABLE message_keys (
 chat_id bigint NOT NULL,
 message_id bigint NOT NULL,
 edit_version integer NOT NULL,
 user_id bigint,
 fingerprint text NOT NULL,
 last_edited_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(chat_id,message_id)
);
CREATE TABLE ai_provider_state (
 id boolean PRIMARY KEY DEFAULT true CHECK(id),
 failures integer NOT NULL DEFAULT 0,
 retry_at timestamptz NOT NULL DEFAULT now()
);


