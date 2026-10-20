-- Fresh public database schema. Contains no user data, deployment roles or migration history.
-- Initialize an empty database with bootstrap.py; do not apply this as an upgrade.

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

CREATE TYPE public."ArticleContentFormat" AS ENUM (
    'MARKDOWN',
    'HTML',
    'RICH_TEXT'
);

CREATE TYPE public."ArticleSourceType" AS ENUM (
    'MANUAL',
    'REPOSITORY_DOC',
    'IMPORTED'
);

CREATE TYPE public."ArticleStatus" AS ENUM (
    'DRAFT',
    'PUBLISHED',
    'ARCHIVED',
    'SCHEDULED',
    'TRASH'
);

CREATE TYPE public."ArticleVisibility" AS ENUM (
    'PUBLIC',
    'AUTHENTICATED'
);

CREATE TYPE public."ConnectionQuality" AS ENUM (
    'EXCELLENT',
    'GOOD',
    'POOR',
    'UNKNOWN'
);

CREATE TYPE public."ConversationStatus" AS ENUM (
    'ACTIVE',
    'COMPLETED',
    'PAUSED',
    'ARCHIVED',
    'ERROR'
);

CREATE TYPE public."DocCategory" AS ENUM (
    'LATEST',
    'FRONTEND',
    'BACKEND',
    'AI',
    'MOBILE',
    'DESIGN'
);

CREATE TYPE public."LaunchType" AS ENUM (
    'NEW_COIN',
    'IDO',
    'BURNING'
);

CREATE TYPE public."MessageRole" AS ENUM (
    'USER',
    'ASSISTANT',
    'SYSTEM',
    'FUNCTION'
);

CREATE TYPE public."ParticipantRole" AS ENUM (
    'INTERVIEWER',
    'INTERVIEWEE',
    'OBSERVER'
);

CREATE TYPE public."RoomStatus" AS ENUM (
    'ACTIVE',
    'ENDED',
    'ARCHIVED'
);

CREATE TYPE public."RoomType" AS ENUM (
    'ONE_ON_ONE',
    'PANEL',
    'GROUP'
);

CREATE TYPE public."TokenStatus" AS ENUM (
    'PENDING',
    'UPCOMING',
    'LAUNCHED',
    'BURNING',
    'FAILED',
    'GRADUATED'
);

CREATE TYPE public."VerificationCodeStatus" AS ENUM (
    'PENDING',
    'VERIFIED',
    'EXPIRED',
    'REVOKED'
);

CREATE TYPE public."VerificationCodeType" AS ENUM (
    'PASSWORD_RESET',
    'EMAIL_LOGIN',
    'EMAIL_VERIFICATION',
    'PHONE_VERIFICATION'
);

CREATE FUNCTION public.campaign_append_only() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN RAISE EXCEPTION 'append-only campaign evidence' USING ERRCODE='23514'; END $$;

CREATE FUNCTION public.campaign_command_attribution_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE command campaign_commands%ROWTYPE; expected_operation text;
BEGIN
  SELECT * INTO command FROM campaign_commands WHERE id=NEW.command_id;
  IF NOT FOUND THEN RAISE EXCEPTION 'campaign command missing' USING ERRCODE='23514'; END IF;
  IF NEW.occurred_at IS DISTINCT FROM command.occurred_at THEN
    RAISE EXCEPTION 'campaign command time mismatch' USING ERRCODE='23514';
  END IF;
  IF TG_TABLE_NAME='campaign_audit_events' THEN
    IF (NEW.actor,NEW.operation) IS DISTINCT FROM (command.actor,command.operation) THEN
      RAISE EXCEPTION 'campaign audit attribution mismatch' USING ERRCODE='23514';
    END IF;
    IF command.operation='create' THEN
      IF command.target_id<>'campaigns' OR (command.result->>'campaignId') IS DISTINCT FROM NEW.campaign_id THEN
        RAISE EXCEPTION 'campaign creation attribution mismatch' USING ERRCODE='23514';
      END IF;
    ELSIF command.target_id<>NEW.campaign_id THEN
      RAISE EXCEPTION 'campaign audit target mismatch' USING ERRCODE='23514';
    END IF;
  ELSIF TG_TABLE_NAME='campaign_publication_history' THEN
    IF command.actor<>NEW.actor OR command.target_id<>NEW.campaign_id OR command.expected_publication_version IS DISTINCT FROM NEW.publication_version-1 OR command.expected_state_version IS NOT NULL THEN
      RAISE EXCEPTION 'publication command attribution mismatch' USING ERRCODE='23514';
    END IF;
    IF command.operation IN ('publish','rollback') THEN
      IF NEW.before_mode NOT IN ('enabled','paused') OR NEW.after_mode<>NEW.before_mode OR NEW.after_revision_id IS NULL THEN
        RAISE EXCEPTION 'publication operation mismatch' USING ERRCODE='23514';
      END IF;
    ELSIF command.operation='pause' THEN
      IF NEW.before_mode<>'enabled' OR NEW.after_mode<>'paused' OR NEW.before_revision_id IS NULL OR NEW.after_revision_id IS DISTINCT FROM NEW.before_revision_id THEN
        RAISE EXCEPTION 'pause operation mismatch' USING ERRCODE='23514';
      END IF;
    ELSIF command.operation='resume' THEN
      IF NEW.before_mode<>'paused' OR NEW.after_mode<>'enabled' OR NEW.before_revision_id IS NULL OR NEW.after_revision_id IS DISTINCT FROM NEW.before_revision_id THEN
        RAISE EXCEPTION 'resume operation mismatch' USING ERRCODE='23514';
      END IF;
    ELSIF command.operation='archive' THEN
      IF NEW.before_mode NOT IN ('enabled','paused') OR NEW.after_mode<>'archived' OR NEW.after_revision_id IS DISTINCT FROM NEW.before_revision_id THEN
        RAISE EXCEPTION 'archive operation mismatch' USING ERRCODE='23514';
      END IF;
    ELSE RAISE EXCEPTION 'command cannot publish a campaign' USING ERRCODE='23514';
    END IF;
  ELSIF TG_TABLE_NAME='campaign_interaction_history' AND command.operation='legacy-import' THEN
    -- An imported legacy state is the administrator's fact about a wallet, bound
    -- to exactly that wallet and kind; it never claims accepted terms.
    IF command.target_id<>NEW.campaign_id OR NEW.acceptance_provenance<>'legacyUnknown' OR NEW.row_version<>1 OR NEW.before_status IS NOT NULL
       OR NEW.after_status NOT IN ('joined','set') OR command.expected_state_version IS DISTINCT FROM 0 OR command.expected_publication_version IS NOT NULL
       OR (command.result->>'walletAddress') IS DISTINCT FROM NEW.wallet_address OR (command.result->>'kind') IS DISTINCT FROM NEW.kind THEN
      RAISE EXCEPTION 'legacy import attribution mismatch' USING ERRCODE='23514';
    END IF;
  ELSE
    expected_operation := CASE
      WHEN NEW.kind='join' AND NEW.after_status='joined' THEN 'join'
      WHEN NEW.kind='join' AND NEW.after_status='left' THEN 'leave'
      WHEN NEW.kind='reminder' AND NEW.after_status='set' THEN 'set-reminder'
      WHEN NEW.kind='reminder' AND NEW.after_status='removed' THEN 'remove-reminder'
      ELSE NULL END;
    IF command.actor<>NEW.wallet_address OR command.target_id<>NEW.campaign_id OR command.operation IS DISTINCT FROM expected_operation OR command.expected_state_version IS DISTINCT FROM NEW.row_version-1 THEN
      RAISE EXCEPTION 'interaction command attribution mismatch' USING ERRCODE='23514';
    END IF;
    IF expected_operation IN ('join','set-reminder') THEN
      IF NEW.acceptance_provenance<>'verified' OR command.expected_publication_version IS DISTINCT FROM NEW.accepted_publication_version THEN
        RAISE EXCEPTION 'interaction accepted publication mismatch' USING ERRCODE='23514';
      END IF;
    ELSIF command.expected_publication_version IS NOT NULL THEN
      RAISE EXCEPTION 'cancellation must not require a publication version' USING ERRCODE='23514';
    END IF;
  END IF;
  RETURN NEW;
END $$;

CREATE FUNCTION public.campaign_evidence_commit_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF TG_TABLE_NAME='campaign_publications' THEN
    IF NEW.publication_version>0 AND NOT EXISTS (SELECT 1 FROM campaign_publication_history h WHERE h.campaign_id=NEW.campaign_id AND h.publication_version=NEW.publication_version AND h.after_mode=NEW.mode AND h.after_revision_id IS NOT DISTINCT FROM NEW.active_revision_id) THEN
      RAISE EXCEPTION 'publication history missing or mismatched' USING ERRCODE='23514';
    END IF;
    IF NEW.interaction_version>0 AND NOT EXISTS (SELECT 1 FROM campaign_interaction_history h WHERE h.campaign_id=NEW.campaign_id AND h.interaction_version=NEW.interaction_version) THEN
      RAISE EXCEPTION 'interaction global version has no history' USING ERRCODE='23514';
    END IF;
  ELSE
    IF NOT EXISTS (SELECT 1 FROM campaign_interaction_history h WHERE (h.campaign_id,h.wallet_address,h.kind,h.row_version)=(NEW.campaign_id,NEW.wallet_address,NEW.kind,NEW.row_version) AND (h.after_status,h.acceptance_provenance,h.accepted_revision_id,h.accepted_publication_version,h.accepted_terms_hash,h.accepted_at) IS NOT DISTINCT FROM (NEW.status,NEW.acceptance_provenance,NEW.accepted_revision_id,NEW.accepted_publication_version,NEW.accepted_terms_hash,NEW.accepted_at)) THEN
      RAISE EXCEPTION 'personal state history missing or mismatched' USING ERRCODE='23514';
    END IF;
  END IF;
  RETURN NULL;
END $$;

CREATE FUNCTION public.campaign_history_commit_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE publication campaign_publications%ROWTYPE;
BEGIN
  SELECT * INTO publication FROM campaign_publications WHERE campaign_id=NEW.campaign_id;
  IF NOT FOUND THEN RAISE EXCEPTION 'history campaign has no publication' USING ERRCODE='23514'; END IF;
  IF TG_TABLE_NAME='campaign_publication_history' THEN
    IF NEW.publication_version>publication.publication_version THEN
      RAISE EXCEPTION 'publication history ahead of current version' USING ERRCODE='23514';
    END IF;
    IF NEW.publication_version=1 THEN
      IF NEW.before_mode<>'enabled' OR NEW.before_revision_id IS NOT NULL THEN
        RAISE EXCEPTION 'first publication predecessor mismatch' USING ERRCODE='23514';
      END IF;
    ELSIF NOT EXISTS (
      SELECT 1 FROM campaign_publication_history h
      WHERE h.campaign_id=NEW.campaign_id AND h.publication_version=NEW.publication_version-1
        AND (h.after_mode,h.after_revision_id) IS NOT DISTINCT FROM (NEW.before_mode,NEW.before_revision_id)
    ) THEN
      RAISE EXCEPTION 'publication history predecessor mismatch' USING ERRCODE='23514';
    END IF;
  ELSE
    IF NEW.interaction_version>publication.interaction_version OR NOT EXISTS (
      SELECT 1 FROM campaign_interaction_states s
      WHERE (s.campaign_id,s.wallet_address,s.kind)=(NEW.campaign_id,NEW.wallet_address,NEW.kind)
        AND s.row_version>=NEW.row_version
    ) THEN
      RAISE EXCEPTION 'interaction history ahead of current version' USING ERRCODE='23514';
    END IF;
    IF NEW.row_version=1 THEN
      IF NEW.before_status IS NOT NULL THEN RAISE EXCEPTION 'first interaction predecessor must be absent' USING ERRCODE='23514'; END IF;
    ELSIF NOT EXISTS (
      SELECT 1 FROM campaign_interaction_history h
      WHERE (h.campaign_id,h.wallet_address,h.kind,h.row_version)=(NEW.campaign_id,NEW.wallet_address,NEW.kind,NEW.row_version-1)
        AND h.after_status IS NOT DISTINCT FROM NEW.before_status AND h.interaction_version<NEW.interaction_version
    ) THEN
      RAISE EXCEPTION 'interaction history predecessor mismatch' USING ERRCODE='23514';
    END IF;
  END IF;
  RETURN NULL;
END $$;

CREATE FUNCTION public.campaign_legacy_activation_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE item campaign_legacy_import_items%ROWTYPE; review campaign_legacy_review_decisions%ROWTYPE;
  command campaign_commands%ROWTYPE; publish campaign_commands%ROWTYPE; imported integer;
BEGIN
  IF TG_OP <> 'INSERT' THEN RAISE EXCEPTION 'legacy activations are evidence' USING ERRCODE = '23514'; END IF;
  PERFORM 1 FROM campaign_legacy_import_runs r WHERE r.id = NEW.run_id AND r.status = 'captured' FOR SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'only a captured run can be activated' USING ERRCODE = '23514'; END IF;
  IF NOT EXISTS (SELECT 1 FROM campaign_legacy_write_fences f WHERE f.run_id = NEW.run_id AND f.campaign_id = NEW.campaign_id AND f.lifted_at IS NULL) THEN
    RAISE EXCEPTION 'an activated campaign must be fenced under its run' USING ERRCODE = '23514';
  END IF;
  SELECT * INTO item FROM campaign_legacy_import_items WHERE run_id = NEW.run_id AND campaign_id = NEW.campaign_id;
  IF NOT FOUND OR (item.legacy_row_sha256, item.interactions_sha256) IS DISTINCT FROM (NEW.legacy_row_sha256, NEW.interactions_sha256) THEN
    RAISE EXCEPTION 'activation does not match the captured plan item' USING ERRCODE = '23514';
  END IF;
  IF item.decision = 'import-public' THEN
    IF NEW.decision <> 'import-public' OR NEW.review_id IS NOT NULL THEN
      RAISE EXCEPTION 'an import-public item activates publicly and without review' USING ERRCODE = '23514';
    END IF;
  ELSIF item.decision = 'review-required' THEN
    SELECT * INTO review FROM campaign_legacy_review_decisions WHERE id = NEW.review_id;
    IF NOT FOUND OR (review.run_id, review.campaign_id, review.decision) IS DISTINCT FROM (NEW.run_id, NEW.campaign_id, NEW.decision) THEN
      RAISE EXCEPTION 'a reviewed item activates only as its recorded decision' USING ERRCODE = '23514';
    END IF;
  ELSE
    RAISE EXCEPTION 'a % plan item cannot be activated', item.decision USING ERRCODE = '23514';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM campaign_definitions d WHERE d.id = NEW.campaign_id AND d.source = 'legacy-unversioned'
      AND d.legacy_snapshot->>'activationId' = NEW.id::text AND d.created_by = NEW.activated_by) THEN
    RAISE EXCEPTION 'activation definition mismatch' USING ERRCODE = '23514';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM campaign_revisions r WHERE r.campaign_id = NEW.campaign_id AND r.id = NEW.revision_id AND r.input->>'templateId' = 'legacyBaselineV1') THEN
    RAISE EXCEPTION 'an activation carries a legacy baseline revision' USING ERRCODE = '23514';
  END IF;
  SELECT * INTO command FROM campaign_commands WHERE id = NEW.command_id;
  IF NOT FOUND OR command.operation <> 'legacy-import' OR command.actor <> NEW.activated_by OR command.target_id <> NEW.campaign_id
     OR command.occurred_at IS DISTINCT FROM NEW.activated_at OR (command.result->>'activationId') IS DISTINCT FROM NEW.id::text
     OR command.result ? 'walletAddress' THEN
    RAISE EXCEPTION 'activation command mismatch' USING ERRCODE = '23514';
  END IF;
  IF NEW.publish_command_id IS NOT NULL THEN
    SELECT * INTO publish FROM campaign_commands WHERE id = NEW.publish_command_id;
    IF NOT FOUND OR publish.operation <> 'publish' OR publish.actor <> NEW.activated_by OR publish.target_id <> NEW.campaign_id
       OR publish.occurred_at IS DISTINCT FROM NEW.activated_at OR publish.expected_publication_version IS DISTINCT FROM 0
       OR NOT EXISTS (SELECT 1 FROM campaign_publication_history h WHERE h.campaign_id = NEW.campaign_id AND h.publication_version = 1
         AND h.command_id = publish.id AND h.after_revision_id = NEW.revision_id) THEN
      RAISE EXCEPTION 'activation publish mismatch' USING ERRCODE = '23514';
    END IF;
  ELSIF EXISTS (SELECT 1 FROM campaign_publication_history h WHERE h.campaign_id = NEW.campaign_id) THEN
    RAISE EXCEPTION 'an unpublished activation must not publish' USING ERRCODE = '23514';
  END IF;
  SELECT count(*) INTO imported FROM campaign_interaction_history h JOIN campaign_commands c ON c.id = h.command_id
    WHERE h.campaign_id = NEW.campaign_id AND c.operation = 'legacy-import' AND (c.result->>'activationId') = NEW.id::text;
  IF imported <> NEW.imported_interactions THEN
    RAISE EXCEPTION 'activation interaction count mismatch' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END $$;

CREATE FUNCTION public.campaign_legacy_capture_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE run campaign_legacy_import_runs%ROWTYPE;
BEGIN
  IF TG_OP = 'UPDATE' THEN RAISE EXCEPTION 'captured legacy evidence is immutable' USING ERRCODE = '23514'; END IF;
  SELECT * INTO run FROM campaign_legacy_import_runs WHERE id = CASE WHEN TG_OP = 'INSERT' THEN NEW.run_id ELSE OLD.run_id END;
  IF TG_OP = 'INSERT' THEN
    IF run.status IS DISTINCT FROM 'capturing' THEN
      RAISE EXCEPTION 'legacy evidence is written only while its run is capturing' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
  END IF;
  IF TG_TABLE_NAME <> 'campaign_legacy_archive_rows' OR run.retain_until > clock_timestamp() THEN
    RAISE EXCEPTION 'legacy evidence is retained until %', run.retain_until USING ERRCODE = '23514';
  END IF;
  RETURN OLD;
END $$;

CREATE FUNCTION public.campaign_legacy_definition_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM campaign_legacy_activations a WHERE a.id::text = NEW.legacy_snapshot->>'activationId' AND a.campaign_id = NEW.id) THEN
    RAISE EXCEPTION 'legacy definition without its activation' USING ERRCODE = '23514';
  END IF;
  RETURN NULL;
END $$;

CREATE FUNCTION public.campaign_legacy_fence_clock_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF TG_OP = 'UPDATE' AND NEW.id = OLD.id AND NEW.ticks = OLD.ticks + 1 THEN RETURN NEW; END IF;
  RAISE EXCEPTION 'the legacy fence clock only advances' USING ERRCODE = '23514';
END $$;

CREATE FUNCTION public.campaign_legacy_fence_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE keys text[]; key text;
BEGIN
  IF TG_TABLE_NAME = 'launchpad_campaigns' THEN
    keys := CASE TG_OP WHEN 'INSERT' THEN ARRAY[NEW.id] WHEN 'DELETE' THEN ARRAY[OLD.id] ELSE ARRAY[OLD.id, NEW.id] END;
  ELSE
    keys := CASE TG_OP WHEN 'INSERT' THEN ARRAY[NEW.campaign_id] WHEN 'DELETE' THEN ARRAY[OLD.campaign_id] ELSE ARRAY[OLD.campaign_id, NEW.campaign_id] END;
  END IF;
  PERFORM campaign_legacy_fence_sync(false);
  FOREACH key IN ARRAY keys LOOP
    PERFORM pg_advisory_xact_lock_shared(hashtextextended('campaign-legacy-fence:' || key, 0));
    IF EXISTS (SELECT 1 FROM campaign_legacy_write_fences f WHERE f.campaign_id = key AND f.lifted_at IS NULL) THEN
      RAISE EXCEPTION 'legacy campaign % is fenced for import', key USING ERRCODE = '55006';
    END IF;
  END LOOP;
  IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
END $$;

CREATE FUNCTION public.campaign_legacy_fence_record_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    -- The run row stays locked until this fence commits: a concurrent abandon
    -- waits for it and is then refused, or has already won and fails it here.
    PERFORM 1 FROM campaign_legacy_import_runs r WHERE r.id = NEW.run_id AND r.status = 'captured' FOR SHARE;
    IF NOT FOUND OR NEW.lifted_at IS NOT NULL OR NOT EXISTS (
      SELECT 1 FROM campaign_legacy_import_items i WHERE i.run_id = NEW.run_id AND i.campaign_id = NEW.campaign_id
        AND i.decision IN ('import-public','import-unpublished','review-required')) THEN
      RAISE EXCEPTION 'only a captured, importable plan item can be fenced' USING ERRCODE = '23514';
    END IF;
    PERFORM campaign_legacy_fence_tick();
    PERFORM pg_advisory_xact_lock(hashtextextended('campaign-legacy-fence:' || NEW.campaign_id, 0));
    IF EXISTS (SELECT 1 FROM campaign_legacy_write_fences f WHERE f.campaign_id = NEW.campaign_id AND f.lifted_at IS NULL) THEN
      RAISE EXCEPTION 'legacy campaign % already has an active fence', NEW.campaign_id USING ERRCODE = '23505';
    END IF;
    RETURN NEW;
  END IF;
  IF TG_OP <> 'UPDATE' THEN RAISE EXCEPTION 'legacy write fences are evidence' USING ERRCODE = '23514'; END IF;
  IF (NEW.campaign_id, NEW.run_id, NEW.fenced_by, NEW.fenced_at) IS DISTINCT FROM (OLD.campaign_id, OLD.run_id, OLD.fenced_by, OLD.fenced_at)
     OR OLD.lifted_at IS NOT NULL OR NEW.lifted_at IS NULL THEN
    RAISE EXCEPTION 'a legacy write fence can only be lifted once' USING ERRCODE = '23514';
  END IF;
  PERFORM campaign_legacy_fence_tick();
  PERFORM pg_advisory_xact_lock(hashtextextended('campaign-legacy-fence:' || NEW.campaign_id, 0));
  -- Checked after the campaign lock, so an activation in progress is waited for.
  IF EXISTS (SELECT 1 FROM campaign_legacy_activations a WHERE a.campaign_id = NEW.campaign_id) THEN
    RAISE EXCEPTION 'an activated legacy campaign stays fenced' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END $$;

CREATE FUNCTION public.campaign_legacy_fence_sync(always boolean) RETURNS void
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF always OR current_setting('transaction_isolation') <> 'read committed' THEN
    PERFORM 1 FROM campaign_legacy_fence_clock WHERE id = 1 FOR SHARE;
    IF NOT FOUND THEN RAISE EXCEPTION 'the legacy fence clock is missing' USING ERRCODE = '55006'; END IF;
  END IF;
END $$;

CREATE FUNCTION public.campaign_legacy_fence_tick() RETURNS void
    LANGUAGE plpgsql
    AS $$
BEGIN
  UPDATE campaign_legacy_fence_clock SET ticks = ticks + 1 WHERE id = 1;
  IF NOT FOUND THEN RAISE EXCEPTION 'the legacy fence clock is missing' USING ERRCODE = '55006'; END IF;
END $$;

CREATE FUNCTION public.campaign_legacy_fence_truncate_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  PERFORM campaign_legacy_fence_sync(true);
  IF EXISTS (SELECT 1 FROM campaign_legacy_write_fences WHERE lifted_at IS NULL) THEN
    RAISE EXCEPTION 'legacy campaign tables cannot be truncated while a fence is active' USING ERRCODE = '55006';
  END IF;
  RETURN NULL;
END $$;

CREATE FUNCTION public.campaign_legacy_import_command_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM campaign_legacy_activations a WHERE a.id::text = NEW.result->>'activationId'
      AND a.campaign_id = NEW.target_id AND a.activated_by = NEW.actor AND a.activated_at = NEW.occurred_at) THEN
    RAISE EXCEPTION 'legacy import command without its activation' USING ERRCODE = '23514';
  END IF;
  RETURN NULL;
END $$;

CREATE FUNCTION public.campaign_legacy_review_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF TG_OP <> 'INSERT' THEN RAISE EXCEPTION 'legacy review decisions are evidence' USING ERRCODE = '23514'; END IF;
  PERFORM 1 FROM campaign_legacy_import_runs r WHERE r.id = NEW.run_id AND r.status = 'captured' FOR SHARE;
  IF NOT FOUND OR NOT EXISTS (SELECT 1 FROM campaign_legacy_import_items i
      WHERE i.run_id = NEW.run_id AND i.campaign_id = NEW.campaign_id AND i.decision = 'review-required') THEN
    RAISE EXCEPTION 'only a review-required item of a captured run can be reviewed' USING ERRCODE = '23514';
  END IF;
  IF NEW.accepted_differences ? 'visibility' AND NEW.decision <> 'import-unpublished' THEN
    RAISE EXCEPTION 'a visibility difference can be accepted only for an unpublished import' USING ERRCODE = '23514';
  END IF;
  IF NEW.decision = 'exclude' AND jsonb_array_length(NEW.accepted_differences) > 0 THEN
    RAISE EXCEPTION 'an excluded item accepts no differences' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END $$;

CREATE FUNCTION public.campaign_legacy_run_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'capturing' OR NEW.snapshot IS NOT NULL OR NEW.source_sha256 IS NOT NULL OR NEW.finished_at IS NOT NULL OR NEW.limit_hit IS NOT NULL THEN
      RAISE EXCEPTION 'a legacy import run starts capturing' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
  END IF;
  IF TG_OP <> 'UPDATE' THEN RAISE EXCEPTION 'legacy import runs are evidence' USING ERRCODE = '23514'; END IF;
  IF (NEW.id, NEW.algorithm, NEW.operator, NEW.limits, NEW.retain_until, NEW.created_at)
     IS DISTINCT FROM (OLD.id, OLD.algorithm, OLD.operator, OLD.limits, OLD.retain_until, OLD.created_at) THEN
    RAISE EXCEPTION 'legacy import run identity is immutable' USING ERRCODE = '23514';
  END IF;
  IF NEW.status = 'abandoned' THEN
    -- A fence being set holds this run row FOR SHARE, so the update waited
    -- for it; the sync covers a snapshot older than a committed fence.
    PERFORM campaign_legacy_fence_sync(false);
    IF EXISTS (SELECT 1 FROM campaign_legacy_write_fences f WHERE f.run_id = NEW.id AND f.lifted_at IS NULL) THEN
      RAISE EXCEPTION 'lift every fence of a legacy import run before abandoning it' USING ERRCODE = '23514';
    END IF;
  END IF;
  IF OLD.status = 'capturing' AND NEW.status IN ('captured','incomplete','abandoned') THEN RETURN NEW; END IF;
  IF OLD.status IN ('captured','incomplete') AND NEW.status = 'abandoned'
     AND (NEW.snapshot, NEW.captured_at, NEW.source_sha256, NEW.row_count, NEW.byte_count, NEW.limit_hit, NEW.finished_at)
         IS NOT DISTINCT FROM (OLD.snapshot, OLD.captured_at, OLD.source_sha256, OLD.row_count, OLD.byte_count, OLD.limit_hit, OLD.finished_at) THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'legacy import run transition % -> % is not allowed', OLD.status, NEW.status USING ERRCODE = '23514';
END $$;

CREATE FUNCTION public.campaign_publication_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.publication_version <> 0 OR NEW.interaction_version <> 0 OR NEW.active_revision_id IS NOT NULL OR NEW.mode <> 'enabled' THEN RAISE EXCEPTION 'initial publication must be unpublished' USING ERRCODE='23514'; END IF;
    RETURN NEW;
  END IF;
  IF TG_OP <> 'UPDATE' THEN RAISE EXCEPTION 'publication cannot be deleted' USING ERRCODE='23514'; END IF;
  IF NEW.campaign_id <> OLD.campaign_id OR NEW.publication_version NOT IN (OLD.publication_version, OLD.publication_version+1) OR NEW.interaction_version NOT IN (OLD.interaction_version, OLD.interaction_version+1) THEN
    RAISE EXCEPTION 'publication identity/version violation' USING ERRCODE='23514';
  END IF;
  IF OLD.mode='archived' AND (NEW.publication_version,NEW.mode,NEW.active_revision_id) IS DISTINCT FROM (OLD.publication_version,OLD.mode,OLD.active_revision_id) THEN
    RAISE EXCEPTION 'archived publication is terminal' USING ERRCODE='23514';
  END IF;
  IF (NEW.mode,NEW.active_revision_id) IS DISTINCT FROM (OLD.mode,OLD.active_revision_id) AND NEW.publication_version<>OLD.publication_version+1 THEN
    RAISE EXCEPTION 'publication transition violation' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END $$;

CREATE FUNCTION public.campaign_state_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.row_version <> 1 THEN RAISE EXCEPTION 'initial state version must be one' USING ERRCODE='23514'; END IF;
    RETURN NEW;
  END IF;
  IF TG_OP <> 'UPDATE' THEN RAISE EXCEPTION 'campaign history identities cannot be deleted' USING ERRCODE='23514'; END IF;
  IF (NEW.campaign_id, NEW.wallet_address, NEW.kind) IS DISTINCT FROM (OLD.campaign_id, OLD.wallet_address, OLD.kind) OR NEW.row_version <> OLD.row_version + 1 THEN
    RAISE EXCEPTION 'interaction identity/version violation' USING ERRCODE='23514';
  END IF;
  IF OLD.acceptance_provenance <> 'neverAccepted' AND NEW.acceptance_provenance = 'neverAccepted' THEN
    RAISE EXCEPTION 'acceptance cannot be erased' USING ERRCODE='23514';
  END IF;
  IF NEW.status IN ('left','removed') AND (NEW.acceptance_provenance,NEW.accepted_revision_id,NEW.accepted_publication_version,NEW.accepted_terms_hash,NEW.accepted_at) IS DISTINCT FROM (OLD.acceptance_provenance,OLD.accepted_revision_id,OLD.accepted_publication_version,OLD.accepted_terms_hash,OLD.accepted_at) THEN
    RAISE EXCEPTION 'cancellation must preserve acceptance' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END $$;

CREATE FUNCTION public.execution_registry_activate(p_expected_version bigint, p_revision bigint, p_actor text, p_reason text, p_code_observations jsonb) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
DECLARE
  current_version BIGINT;
  current_revision BIGINT;
  next_version BIGINT;
  header record;
BEGIN
  PERFORM pg_advisory_xact_lock(hashtext('execution_registry_activation'));
  SELECT activation_version, active_revision INTO current_version, current_revision FROM public.execution_registry_pointer WHERE id = 1 FOR UPDATE;
  IF NOT FOUND THEN current_version := 0; END IF;
  IF p_expected_version IS DISTINCT FROM current_version THEN
    RAISE EXCEPTION 'activation version is %, expected %', current_version, p_expected_version USING ERRCODE = 'EXR02';
  END IF;
  SELECT sealed, content_digest INTO header FROM public.execution_registry_snapshots WHERE revision = p_revision;
  IF NOT FOUND OR NOT header.sealed THEN
    RAISE EXCEPTION 'revision % is not a sealed snapshot', p_revision USING ERRCODE = 'EXR03';
  END IF;
  IF header.content_digest IS DISTINCT FROM public.execution_registry_content_digest(p_revision) THEN
    RAISE EXCEPTION 'revision % no longer matches its content digest', p_revision USING ERRCODE = 'EXR03';
  END IF;
  PERFORM public.execution_registry_validate(p_revision);
  next_version := current_version + 1;
  INSERT INTO public.execution_registry_activations (activation_version, revision, previous_revision, activated_by, reason, code_observations)
  VALUES (next_version, p_revision, current_revision, p_actor, p_reason, p_code_observations);
  IF current_version = 0 THEN
    INSERT INTO public.execution_registry_pointer (id, active_revision, activation_version) VALUES (1, p_revision, next_version);
  ELSE
    UPDATE public.execution_registry_pointer SET active_revision = p_revision, activation_version = next_version WHERE id = 1;
  END IF;
  RETURN next_version;
END $$;

CREATE FUNCTION public.execution_registry_activation_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
BEGIN
  IF TG_OP <> 'INSERT' THEN
    RAISE EXCEPTION 'activation history is append-only' USING ERRCODE = 'EXR01';
  END IF;
  IF NEW.xid <> pg_current_xact_id() THEN
    RAISE EXCEPTION 'activation record must belong to its transaction' USING ERRCODE = 'EXR04';
  END IF;
  RETURN NEW;
END $$;

CREATE FUNCTION public.execution_registry_canonical(p_value jsonb) RETURNS text
    LANGUAGE plpgsql IMMUTABLE
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
DECLARE result TEXT;
BEGIN
  CASE jsonb_typeof(p_value)
    WHEN 'object' THEN
      SELECT '{' || coalesce(string_agg(to_jsonb(key)::text || ':' || public.execution_registry_canonical(value), ',' ORDER BY key COLLATE "C"), '') || '}'
      INTO result FROM jsonb_each(p_value);
    WHEN 'array' THEN
      SELECT '[' || coalesce(string_agg(public.execution_registry_canonical(value), ',' ORDER BY ordinal), '') || ']'
      INTO result FROM jsonb_array_elements(p_value) WITH ORDINALITY AS items(value, ordinal);
    ELSE result := p_value::text;
  END CASE;
  RETURN replace(replace(result, chr(8232), E'\\u2028'), chr(8233), E'\\u2029');
END $$;

CREATE FUNCTION public.execution_registry_content_digest(p_revision bigint) RETURNS text
    LANGUAGE sql STABLE
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
  SELECT encode(sha256(convert_to('execution-registry-content/v1' || E'\n'
           || coalesce(string_agg(line, E'\n' ORDER BY line COLLATE "C"), ''), 'UTF8')), 'hex')
  FROM (
    SELECT 'e:' || entry_digest AS line FROM public.execution_registry_entries WHERE revision = p_revision
    UNION ALL SELECT 'm:' || market_digest FROM public.execution_registry_markets WHERE revision = p_revision
    UNION ALL SELECT 'b:' || binding_digest FROM public.execution_registry_bindings WHERE revision = p_revision
  ) members
$$;

CREATE FUNCTION public.execution_registry_header_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
BEGIN
  IF NEW.sealed OR NEW.content_digest IS NOT NULL OR NEW.builder_xid <> pg_current_xact_id() THEN
    RAISE EXCEPTION 'snapshot must be built and sealed by its inserting transaction' USING ERRCODE = 'EXR03';
  END IF;
  RETURN NEW;
END $$;

CREATE FUNCTION public.execution_registry_identity_digest(p_value jsonb) RETURNS text
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
  SELECT encode(sha256(convert_to(public.execution_registry_canonical(p_value), 'UTF8')), 'hex')
$$;

CREATE FUNCTION public.execution_registry_immutable() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
BEGIN
  RAISE EXCEPTION '% on % is not allowed: registry history is immutable', TG_OP, TG_TABLE_NAME USING ERRCODE = 'EXR01';
END $$;

CREATE FUNCTION public.execution_registry_member_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
DECLARE header record;
BEGIN
  IF TG_OP <> 'INSERT' THEN
    RAISE EXCEPTION '% on % is not allowed: snapshot members are immutable', TG_OP, TG_TABLE_NAME USING ERRCODE = 'EXR01';
  END IF;
  SELECT sealed, builder_xid INTO header FROM public.execution_registry_snapshots WHERE revision = NEW.revision;
  IF NOT FOUND OR header.sealed OR header.builder_xid <> pg_current_xact_id() THEN
    RAISE EXCEPTION 'revision % accepts no new members', NEW.revision USING ERRCODE = 'EXR01';
  END IF;
  RETURN NEW;
END $$;

CREATE FUNCTION public.execution_registry_pointer_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
BEGIN
  IF TG_OP NOT IN ('INSERT', 'UPDATE') THEN
    RAISE EXCEPTION '% on execution_registry_pointer is not allowed', TG_OP USING ERRCODE = 'EXR01';
  END IF;
  IF (TG_OP = 'INSERT' AND NEW.activation_version <> 1)
     OR (TG_OP = 'UPDATE' AND (NEW.id <> OLD.id OR NEW.activation_version <> OLD.activation_version + 1)) THEN
    RAISE EXCEPTION 'activation versions advance by one' USING ERRCODE = 'EXR04';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM public.execution_registry_activations a
                 WHERE a.activation_version = NEW.activation_version AND a.revision = NEW.active_revision AND a.xid = pg_current_xact_id())
     OR NOT EXISTS (SELECT 1 FROM public.execution_registry_snapshots s WHERE s.revision = NEW.active_revision AND s.sealed) THEN
    RAISE EXCEPTION 'the pointer changes only with its activation record' USING ERRCODE = 'EXR04';
  END IF;
  RETURN NEW;
END $$;

CREATE FUNCTION public.execution_registry_seal_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
BEGIN
  IF TG_OP <> 'UPDATE' THEN
    RAISE EXCEPTION '% on execution_registry_snapshots is not allowed', TG_OP USING ERRCODE = 'EXR01';
  END IF;
  IF OLD.sealed OR NOT NEW.sealed OR OLD.builder_xid <> pg_current_xact_id()
     OR NEW.revision <> OLD.revision OR NEW.schema_version <> OLD.schema_version OR NEW.manifest_digest <> OLD.manifest_digest
     OR NEW.declarations_digest <> OLD.declarations_digest OR NEW.builder_xid <> OLD.builder_xid OR NEW.source_summary <> OLD.source_summary
     OR NEW.staged_at <> OLD.staged_at OR NEW.staged_by <> OLD.staged_by OR NEW.reason <> OLD.reason THEN
    RAISE EXCEPTION 'a snapshot can only be sealed once, by the transaction that staged it' USING ERRCODE = 'EXR01';
  END IF;
  PERFORM public.execution_registry_validate(NEW.revision);
  IF NEW.content_digest IS DISTINCT FROM public.execution_registry_content_digest(NEW.revision) THEN
    RAISE EXCEPTION 'content digest does not match the staged members of revision %', NEW.revision USING ERRCODE = 'EXR03';
  END IF;
  RETURN NEW;
END $$;

CREATE FUNCTION public.execution_registry_sealed_at_commit() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
BEGIN
  IF NOT (SELECT sealed FROM public.execution_registry_snapshots WHERE revision = NEW.revision) THEN
    RAISE EXCEPTION 'revision % was not sealed before commit', NEW.revision USING ERRCODE = 'EXR03';
  END IF;
  RETURN NULL;
END $$;

CREATE FUNCTION public.execution_registry_validate(p_revision bigint) RETURNS void
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
DECLARE
  e record; m record; b record; target record;
  expected TEXT; pm TEXT; idx TEXT; collateral TEXT; market_hash TEXT;
  components JSONB; roles JSONB; expected_roles JSONB; target_name TEXT; interface_name TEXT;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM public.execution_registry_snapshots WHERE revision = p_revision AND schema_version = 1
      AND manifest_digest = '980ec8cf99e77279852af3cc9e413b7680bbbfa72e1fca576720a3415872f926') THEN
    RAISE EXCEPTION 'unsupported snapshot schema or manifest' USING ERRCODE = 'EXR03';
  END IF;
  FOR e IN SELECT * FROM public.execution_registry_entries WHERE revision = p_revision LOOP
    IF e.interfaces IS DISTINCT FROM ARRAY(SELECT DISTINCT id COLLATE "C" FROM unnest(e.interfaces) AS ids(id) ORDER BY id COLLATE "C")
       OR EXISTS (SELECT 1 FROM unnest(e.interfaces) AS ids(id) WHERE id IS NULL OR id NOT IN ('erc20.approve.v1','perp.positionManager.open.v1','perp.positionManager.close.v1')) THEN
      RAISE EXCEPTION 'unsupported or noncanonical entry interfaces' USING ERRCODE = 'EXR03';
    END IF;
    expected := public.execution_registry_identity_digest(jsonb_build_object('chainId',e.chain_id::text,'name',e.name,'address',e.address,
      'deploymentIdentity',jsonb_build_object('file',e.deployment_file,'timestamp',e.deployment_timestamp),
      'codeHash',e.code_hash,'interfaces',to_jsonb(e.interfaces)));
    IF e.entry_digest IS DISTINCT FROM expected THEN
      RAISE EXCEPTION 'entry digest does not describe its fields' USING ERRCODE = 'EXR03';
    END IF;
  END LOOP;
  FOR m IN SELECT * FROM public.execution_registry_markets WHERE revision = p_revision LOOP
    IF m.price_precision <> 30 OR m.components IS DISTINCT FROM ARRAY(SELECT DISTINCT name COLLATE "C" FROM unnest(m.components) AS names(name) ORDER BY name COLLATE "C")
      OR EXISTS (SELECT 1 FROM unnest(m.components) AS names(name) WHERE name IS NULL OR NOT EXISTS
        (SELECT 1 FROM public.execution_registry_entries WHERE revision = p_revision AND chain_id = m.chain_id AND execution_registry_entries.name = names.name)) THEN
      RAISE EXCEPTION 'invalid market dependencies or precision' USING ERRCODE = 'EXR03';
    END IF;
    SELECT entry_digest INTO STRICT pm FROM public.execution_registry_entries WHERE revision = p_revision AND chain_id = m.chain_id AND name = m.position_manager;
    SELECT entry_digest INTO STRICT idx FROM public.execution_registry_entries WHERE revision = p_revision AND chain_id = m.chain_id AND name = m.index_token;
    SELECT entry_digest INTO STRICT collateral FROM public.execution_registry_entries WHERE revision = p_revision AND chain_id = m.chain_id AND name = m.collateral_token;
    SELECT coalesce(jsonb_agg(entry_digest ORDER BY name COLLATE "C"),'[]'::jsonb) INTO components
      FROM public.execution_registry_entries WHERE revision = p_revision AND chain_id = m.chain_id AND name = ANY(m.components);
    expected := public.execution_registry_identity_digest(jsonb_build_object('chainId',m.chain_id::text,'symbol',m.symbol,
      'positionManager',pm,'indexToken',idx,'collateralToken',collateral,'indexDecimals',m.index_decimals::text,
      'collateralDecimals',m.collateral_decimals::text,'pricePrecision',m.price_precision::text,'components',components));
    IF m.market_digest IS DISTINCT FROM expected THEN
      RAISE EXCEPTION 'market digest does not describe its dependencies' USING ERRCODE = 'EXR03';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM public.execution_registry_bindings WHERE revision = p_revision AND chain_id = m.chain_id
        AND market = m.symbol AND operation = 'trade.closePosition' AND status = 'enabled') THEN
      RAISE EXCEPTION 'market has no enabled exit' USING ERRCODE = 'EXR03';
    END IF;
  END LOOP;
  FOR b IN SELECT * FROM public.execution_registry_bindings WHERE revision = p_revision LOOP
    SELECT * INTO STRICT m FROM public.execution_registry_markets WHERE revision = p_revision AND chain_id = b.chain_id AND symbol = b.market;
    IF b.operation = 'trade.approveCollateral' THEN
      target_name := m.collateral_token; interface_name := 'erc20.approve.v1'; expected_roles := jsonb_build_object('spender',m.position_manager);
    ELSE
      target_name := m.position_manager;
      interface_name := CASE b.operation WHEN 'trade.openPosition' THEN 'perp.positionManager.open.v1' ELSE 'perp.positionManager.close.v1' END;
      expected_roles := jsonb_build_object('indexToken',m.index_token,'collateralToken',m.collateral_token);
    END IF;
    SELECT * INTO STRICT target FROM public.execution_registry_entries WHERE revision = p_revision AND chain_id = b.chain_id AND name = target_name;
    IF b.target_name IS DISTINCT FROM target_name OR b.interface_id IS DISTINCT FROM interface_name OR b.roles IS DISTINCT FROM expected_roles
        OR NOT (b.interface_id = ANY(target.interfaces)) THEN
      RAISE EXCEPTION 'binding target, roles or semantic interface disagree' USING ERRCODE = 'EXR03';
    END IF;
    SELECT coalesce(jsonb_object_agg(role.key, entry.entry_digest),'{}'::jsonb) INTO roles FROM jsonb_each_text(b.roles) role
      JOIN public.execution_registry_entries entry ON entry.revision = p_revision AND entry.chain_id = b.chain_id AND entry.name = role.value;
    expected := public.execution_registry_identity_digest(jsonb_build_object('schema','execution-binding/v1','chainId',b.chain_id::text,
      'operation',b.operation,'market',b.market,'interfaceId',b.interface_id,'target',target.entry_digest,'roles',roles,'marketDigest',m.market_digest));
    IF b.binding_digest IS DISTINCT FROM expected THEN
      RAISE EXCEPTION 'binding digest does not describe its dependencies' USING ERRCODE = 'EXR03';
    END IF;
  END LOOP;
END $$;

CREATE FUNCTION public.outbox_event_commit_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM outbox_streams s WHERE s.source=NEW.source AND s.stream_key=NEW.aggregate_id
      AND s.last_stream_version>=NEW.aggregate_version
  ) THEN
    RAISE EXCEPTION 'outbox event ahead of allocated stream version' USING ERRCODE='23514';
  END IF;
  RETURN NULL;
END $$;

CREATE FUNCTION public.outbox_payload_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF TG_OP <> 'UPDATE' THEN RAISE EXCEPTION 'outbox evidence cannot be deleted' USING ERRCODE='23514'; END IF;
  IF (NEW.id,NEW.source,NEW.aggregate_id,NEW.aggregate_version,NEW.topic,NEW.owner_scope,NEW.payload_version,NEW.payload,NEW.occurred_at) IS DISTINCT FROM (OLD.id,OLD.source,OLD.aggregate_id,OLD.aggregate_version,OLD.topic,OLD.owner_scope,OLD.payload_version,OLD.payload,OLD.occurred_at) THEN
    RAISE EXCEPTION 'outbox payload is immutable' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END $$;

CREATE FUNCTION public.outbox_replay_immutable() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
BEGIN RAISE EXCEPTION 'outbox maintenance audit is append-only' USING ERRCODE='23514'; END $$;

CREATE FUNCTION public.outbox_replay_intent_stamp() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
BEGIN
 NEW.accepted_at:=clock_timestamp();
 IF NOT EXISTS(SELECT 1 FROM public.durable_outbox WHERE id=NEW.event_id AND source='campaign') THEN
  RAISE EXCEPTION 'maintenance target is not a Campaign event' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;

CREATE FUNCTION public.outbox_replay_outcome_commit_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE event RECORD; intent public.outbox_replay_intents; actual JSONB;
BEGIN
 SELECT replay_generation,replay_transaction,attempts,dead_lettered_at,error_code,published_at,available_at,lease_owner,lease_until
 INTO STRICT event FROM public.durable_outbox WHERE id=NEW.event_id;
 SELECT * INTO STRICT intent FROM public.outbox_replay_intents WHERE command_id=NEW.command_id;
 actual:=public.outbox_replay_snapshot(event.replay_generation,event.attempts,event.dead_lettered_at,event.error_code,event.published_at,event.available_at,event.lease_until,NEW.completed_at);
 IF NEW.write_transaction<>pg_current_xact_id()::text OR NEW.after_state IS DISTINCT FROM actual OR NEW.replay_generation<>event.replay_generation THEN
  RAISE EXCEPTION 'maintenance outcome disagrees with the committed event' USING ERRCODE='23514';
 END IF;
 IF NEW.status='succeeded' THEN
  IF event.replay_transaction IS DISTINCT FROM NEW.write_transaction OR event.replay_generation<>intent.expected_generation+1
   OR event.dead_lettered_at IS NOT NULL OR event.published_at IS NOT NULL OR event.attempts<>0
   OR event.error_code IS NOT NULL OR event.lease_owner IS NOT NULL OR event.lease_until IS NOT NULL OR event.available_at<>NEW.completed_at
   OR NEW.before_state IS DISTINCT FROM jsonb_build_object('generation',intent.expected_generation,'attempts',intent.expected_attempts,
    'deadLetteredAt',to_char(timezone('UTC',intent.expected_dead_lettered_at),'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
    'errorCode',intent.expected_error_code,'publishedAt',NULL,'availableAt',NEW.before_state->'availableAt','leaseActive',false) THEN
   RAISE EXCEPTION 'success does not prove a matching replay transition' USING ERRCODE='23514';
  END IF;
 ELSE
  IF NEW.before_state IS DISTINCT FROM actual OR (event.replay_generation=intent.expected_generation
    AND event.dead_lettered_at=intent.expected_dead_lettered_at AND event.attempts=intent.expected_attempts
    AND event.error_code=intent.expected_error_code AND event.published_at IS NULL AND (event.lease_until IS NULL OR event.lease_until<=NEW.completed_at)) THEN
   RAISE EXCEPTION 'refusal does not prove a changed replay precondition' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NULL;
END $$;

CREATE FUNCTION public.outbox_replay_outcome_stamp() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE event RECORD;
BEGIN
 NEW.write_transaction:=pg_current_xact_id()::text;
 SELECT replay_generation,attempts,dead_lettered_at,error_code,published_at,available_at,lease_until
 INTO STRICT event FROM public.durable_outbox WHERE id=NEW.event_id FOR UPDATE;
 IF NEW.status='succeeded' THEN NEW.completed_at:=event.available_at;
 ELSE
  NEW.completed_at:=clock_timestamp();
  NEW.before_state:=public.outbox_replay_snapshot(event.replay_generation,event.attempts,event.dead_lettered_at,event.error_code,event.published_at,event.available_at,event.lease_until,NEW.completed_at);
  NEW.after_state:=NEW.before_state;
 END IF;
 RETURN NEW;
END $$;

CREATE FUNCTION public.outbox_replay_reset_commit_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
BEGIN
 IF OLD.dead_lettered_at IS NULL OR NEW.dead_lettered_at IS NOT NULL THEN RETURN NULL; END IF;
 IF NOT EXISTS (
  SELECT 1 FROM public.outbox_replay_outcomes o JOIN public.outbox_replay_intents i USING(command_id,event_id)
  WHERE o.event_id=NEW.id AND o.status='succeeded' AND o.replay_generation=NEW.replay_generation
   AND o.write_transaction=pg_current_xact_id()::text AND NEW.replay_transaction=o.write_transaction
   AND i.expected_generation=OLD.replay_generation AND i.expected_dead_lettered_at=OLD.dead_lettered_at
   AND i.expected_attempts=OLD.attempts AND i.expected_error_code=OLD.error_code
   AND i.operation='campaign.outbox.replay' AND i.source='campaign'
   AND NEW.available_at=o.completed_at
   AND o.before_state=public.outbox_replay_snapshot(OLD.replay_generation,OLD.attempts,OLD.dead_lettered_at,OLD.error_code,OLD.published_at,OLD.available_at,OLD.lease_until,o.completed_at)
   AND o.after_state=public.outbox_replay_snapshot(NEW.replay_generation,NEW.attempts,NEW.dead_lettered_at,NEW.error_code,NEW.published_at,NEW.available_at,NEW.lease_until,o.completed_at)
 ) THEN RAISE EXCEPTION 'replay reset has no matching atomic audit' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;

CREATE FUNCTION public.outbox_replay_snapshot(g bigint, a integer, d timestamp with time zone, e text, p timestamp with time zone, v timestamp with time zone, l timestamp with time zone, observed timestamp with time zone) RETURNS jsonb
    LANGUAGE sql STABLE
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
 SELECT jsonb_build_object('generation',g,'attempts',a,
  'deadLetteredAt',to_char(timezone('UTC',d),'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
  'errorCode',CASE WHEN e IN ('publisherUnavailable','unsupportedSchema','leaseExpired') THEN e END,
  'publishedAt',to_char(timezone('UTC',p),'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
  'availableAt',to_char(timezone('UTC',v),'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
  'leaseActive',COALESCE(l>observed,false))
$$;

CREATE FUNCTION public.outbox_replay_transition_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.replay_generation<>0 OR NEW.replay_transaction IS NOT NULL THEN
   RAISE EXCEPTION 'new events cannot fabricate recovery history' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
 END IF;
 IF OLD.dead_lettered_at IS NOT NULL AND NEW.dead_lettered_at IS NULL THEN
  IF OLD.source<>'campaign' OR OLD.published_at IS NOT NULL OR OLD.lease_until>clock_timestamp()
   OR NEW.replay_generation<>OLD.replay_generation+1 OR NEW.published_at IS NOT NULL
   OR NEW.attempts<>0 OR NEW.error_code IS NOT NULL OR NEW.lease_owner IS NOT NULL OR NEW.lease_until IS NOT NULL THEN
   RAISE EXCEPTION 'dead-letter reset requires an audited replay incarnation' USING ERRCODE='23514';
  END IF;
  NEW.replay_transaction:=pg_current_xact_id()::text;
  NEW.available_at:=clock_timestamp();
 ELSIF (NEW.replay_generation,NEW.replay_transaction) IS DISTINCT FROM (OLD.replay_generation,OLD.replay_transaction) THEN
  RAISE EXCEPTION 'recovery identity can advance only with a dead-letter reset' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;

CREATE FUNCTION public.outbox_stream_commit_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM durable_outbox o WHERE o.aggregate_id=NEW.stream_key AND o.aggregate_version=NEW.last_stream_version AND o.source=NEW.source) THEN
    RAISE EXCEPTION 'stream version has no durable event' USING ERRCODE='23514';
  END IF;
  RETURN NULL;
END $$;

CREATE FUNCTION public.outbox_stream_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.last_stream_version <> 1 THEN RAISE EXCEPTION 'initial stream version must be one' USING ERRCODE='23514'; END IF;
    RETURN NEW;
  END IF;
  IF TG_OP <> 'UPDATE' THEN RAISE EXCEPTION 'stream cannot be deleted' USING ERRCODE='23514'; END IF;
  IF (NEW.stream_key,NEW.source) IS DISTINCT FROM (OLD.stream_key,OLD.source) OR NEW.last_stream_version<>OLD.last_stream_version+1 THEN
    RAISE EXCEPTION 'stream identity/version violation' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END $$;

CREATE FUNCTION public.web3_auth_session_guard() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF NEW.id <> OLD.id OR NEW.wallet_address <> OLD.wallet_address OR NEW.chain_id <> OLD.chain_id
     OR NEW.auth_epoch <> OLD.auth_epoch OR NEW.key_id <> OLD.key_id OR NEW.issued_at <> OLD.issued_at
     OR NEW.expires_at <> OLD.expires_at OR NEW.created_at <> OLD.created_at OR NEW.session_version < OLD.session_version THEN
    RAISE EXCEPTION 'web3 session identity is immutable' USING ERRCODE = '23514';
  END IF;
  IF OLD.revoked_at IS NOT NULL AND (NEW.revoked_at IS DISTINCT FROM OLD.revoked_at OR NEW.revoke_reason IS DISTINCT FROM OLD.revoke_reason OR NEW.revoked_by IS DISTINCT FROM OLD.revoked_by OR NEW.session_version <> OLD.session_version) THEN
    RAISE EXCEPTION 'web3 session revocation is one-way' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END $$;

SET default_tablespace = '';

SET default_table_access_method = heap;

CREATE TABLE public."_ArticleToTag" (
    "A" text NOT NULL,
    "B" text NOT NULL
);

CREATE TABLE public.articles (
    id text NOT NULL,
    title text NOT NULL,
    slug text,
    content text NOT NULL,
    summary text,
    published boolean DEFAULT false NOT NULL,
    status public."ArticleStatus" DEFAULT 'DRAFT'::public."ArticleStatus",
    "lastSavedAt" timestamp(3) without time zone,
    version integer DEFAULT 1,
    "draftContent" text,
    "metaTitle" text,
    "metaDescription" text,
    keywords text,
    "featuredImage" text,
    "viewCount" integer DEFAULT 0 NOT NULL,
    "authorId" text NOT NULL,
    "categoryId" text,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL,
    "publishedAt" timestamp(3) without time zone,
    "contentFormat" public."ArticleContentFormat" DEFAULT 'MARKDOWN'::public."ArticleContentFormat",
    "sourceCollection" text,
    "sourcePath" text,
    "sourceType" public."ArticleSourceType" DEFAULT 'MANUAL'::public."ArticleSourceType",
    visibility public."ArticleVisibility" DEFAULT 'PUBLIC'::public."ArticleVisibility" NOT NULL
);

CREATE TABLE public.auth_sessions (
    id text NOT NULL,
    "userId" text NOT NULL,
    "refreshTokenHash" text NOT NULL,
    "expiresAt" timestamp(3) without time zone NOT NULL,
    "lastUsedAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "revokedAt" timestamp(3) without time zone,
    "userAgent" text,
    "ipAddressHash" text,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.bridge_relayer_heartbeat (
    relayer_address character varying(42) NOT NULL,
    last_seen_at timestamp(3) without time zone NOT NULL,
    chains integer[] DEFAULT ARRAY[]::integer[] NOT NULL,
    gas_balances jsonb,
    can_deliver boolean DEFAULT false NOT NULL,
    last_cycle_error text,
    created_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);

CREATE TABLE public.bridge_relayer_log_failures (
    chain_id integer NOT NULL,
    block_number bigint NOT NULL,
    log_index integer NOT NULL,
    tx_hash character varying(66) NOT NULL,
    last_error text NOT NULL,
    attempts integer DEFAULT 1 NOT NULL,
    first_failed_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    last_failed_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    resolved_at timestamp(3) without time zone,
    CONSTRAINT bridge_relayer_log_failures_attempts_check CHECK ((attempts > 0)),
    CONSTRAINT bridge_relayer_log_failures_block_number_check CHECK ((block_number >= 0)),
    CONSTRAINT bridge_relayer_log_failures_log_index_check CHECK ((log_index >= 0))
);

CREATE TABLE public.bridge_relayer_scan_progress (
    chain_id integer NOT NULL,
    next_block bigint NOT NULL,
    created_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp(3) without time zone NOT NULL,
    CONSTRAINT bridge_relayer_scan_progress_next_block_check CHECK ((next_block >= 0))
);

CREATE TABLE public.bridge_transfers (
    id text NOT NULL,
    transfer_id character varying(66) NOT NULL,
    src_chain_id integer NOT NULL,
    dst_chain_id integer NOT NULL,
    src_gateway character varying(42) NOT NULL,
    sender character varying(42) NOT NULL,
    recipient character varying(42) NOT NULL,
    src_token character varying(42) NOT NULL,
    dst_token character varying(42),
    amount numeric(78,0) NOT NULL,
    status character varying(24) NOT NULL,
    deposit_tx_hash character varying(66) NOT NULL,
    deposit_log_index integer NOT NULL,
    deposit_block bigint NOT NULL,
    deposited_at timestamp(3) without time zone NOT NULL,
    fulfill_tx_hash character varying(66),
    fulfill_block bigint,
    fulfilled_at timestamp(3) without time zone,
    refund_tx_hash character varying(66),
    refunded_at timestamp(3) without time zone,
    last_error text,
    attempts integer DEFAULT 0 NOT NULL,
    created_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.campaign_audit_events (
    id uuid NOT NULL,
    campaign_id text NOT NULL,
    command_id uuid NOT NULL,
    actor character varying(42) NOT NULL,
    operation character varying(32) NOT NULL,
    changes jsonb NOT NULL,
    occurred_at timestamp(6) with time zone NOT NULL
);

CREATE TABLE public.campaign_commands (
    id uuid NOT NULL,
    actor character varying(42) NOT NULL,
    operation character varying(32) NOT NULL,
    target_id character varying(160) NOT NULL,
    client_command_id uuid NOT NULL,
    payload_hash character varying(64) NOT NULL,
    expected_publication_version bigint,
    expected_state_version bigint,
    reason character varying(1000) NOT NULL,
    request_id character varying(96) NOT NULL,
    result jsonb NOT NULL,
    occurred_at timestamp(6) with time zone NOT NULL,
    CONSTRAINT campaign_command_operation CHECK (((operation)::text = ANY ((ARRAY['create'::character varying, 'revision'::character varying, 'publish'::character varying, 'rollback'::character varying, 'pause'::character varying, 'resume'::character varying, 'archive'::character varying, 'join'::character varying, 'leave'::character varying, 'set-reminder'::character varying, 'remove-reminder'::character varying, 'legacy-import'::character varying])::text[]))),
    CONSTRAINT campaign_commands_actor_check CHECK (((actor)::text ~ '^0x[0-9a-f]{40}$'::text)),
    CONSTRAINT campaign_commands_expected_publication_version_check CHECK ((expected_publication_version >= 0)),
    CONSTRAINT campaign_commands_expected_state_version_check CHECK ((expected_state_version >= 0)),
    CONSTRAINT campaign_commands_payload_hash_check CHECK (((payload_hash)::text ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT campaign_commands_result_check CHECK ((jsonb_typeof(result) = 'object'::text))
);

CREATE TABLE public.campaign_definitions (
    id text NOT NULL,
    slug character varying(100),
    source character varying(24) DEFAULT 'created'::character varying NOT NULL,
    legacy_snapshot jsonb,
    created_by character varying(96) NOT NULL,
    created_at timestamp(6) with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT campaign_definition_snapshot CHECK (((((source)::text = 'created'::text) AND (legacy_snapshot IS NULL)) OR (((source)::text = 'legacy-unversioned'::text) AND (legacy_snapshot IS NOT NULL)))),
    CONSTRAINT campaign_definition_source CHECK (((source)::text = ANY ((ARRAY['created'::character varying, 'legacy-unversioned'::character varying])::text[])))
);

CREATE TABLE public.campaign_interaction_history (
    campaign_id text NOT NULL,
    wallet_address character varying(42) NOT NULL,
    kind character varying(16) NOT NULL,
    row_version bigint NOT NULL,
    interaction_version bigint NOT NULL,
    command_id uuid NOT NULL,
    before_status character varying(16),
    after_status character varying(16) NOT NULL,
    acceptance_provenance character varying(16) NOT NULL,
    accepted_revision_id uuid,
    accepted_publication_version bigint,
    accepted_terms_hash character varying(64),
    accepted_at timestamp(6) with time zone,
    occurred_at timestamp(6) with time zone NOT NULL,
    CONSTRAINT campaign_interaction_history_check CHECK (((before_status IS NULL) OR (((kind)::text = 'join'::text) AND ((before_status)::text = ANY ((ARRAY['joined'::character varying, 'left'::character varying])::text[]))) OR (((kind)::text = 'reminder'::text) AND ((before_status)::text = ANY ((ARRAY['set'::character varying, 'removed'::character varying])::text[]))))),
    CONSTRAINT campaign_interaction_history_check1 CHECK (((((kind)::text = 'join'::text) AND ((after_status)::text = ANY ((ARRAY['joined'::character varying, 'left'::character varying])::text[]))) OR (((kind)::text = 'reminder'::text) AND ((after_status)::text = ANY ((ARRAY['set'::character varying, 'removed'::character varying])::text[]))))),
    CONSTRAINT campaign_interaction_history_check2 CHECK (((((acceptance_provenance)::text = 'verified'::text) AND (accepted_revision_id IS NOT NULL) AND (accepted_publication_version IS NOT NULL) AND (accepted_publication_version > 0) AND (accepted_terms_hash IS NOT NULL) AND ((accepted_terms_hash)::text ~ '^[0-9a-f]{64}$'::text) AND (accepted_at IS NOT NULL)) OR (((acceptance_provenance)::text = 'neverAccepted'::text) AND ((after_status)::text = ANY ((ARRAY['left'::character varying, 'removed'::character varying])::text[])) AND (accepted_revision_id IS NULL) AND (accepted_publication_version IS NULL) AND (accepted_terms_hash IS NULL) AND (accepted_at IS NULL)) OR (((acceptance_provenance)::text = 'legacyUnknown'::text) AND (accepted_revision_id IS NULL) AND (accepted_publication_version IS NULL) AND (accepted_terms_hash IS NULL)))),
    CONSTRAINT campaign_interaction_history_interaction_version_check CHECK ((interaction_version > 0)),
    CONSTRAINT campaign_interaction_history_row_version_check CHECK ((row_version > 0))
);

CREATE TABLE public.campaign_interaction_states (
    campaign_id text NOT NULL,
    wallet_address character varying(42) NOT NULL,
    kind character varying(16) NOT NULL,
    status character varying(16) NOT NULL,
    row_version bigint NOT NULL,
    acceptance_provenance character varying(16) NOT NULL,
    accepted_revision_id uuid,
    accepted_publication_version bigint,
    accepted_terms_hash character varying(64),
    accepted_at timestamp(6) with time zone,
    changed_at timestamp(6) with time zone NOT NULL,
    CONSTRAINT campaign_interaction_states_check CHECK (((((kind)::text = 'join'::text) AND ((status)::text = ANY ((ARRAY['joined'::character varying, 'left'::character varying])::text[]))) OR (((kind)::text = 'reminder'::text) AND ((status)::text = ANY ((ARRAY['set'::character varying, 'removed'::character varying])::text[]))))),
    CONSTRAINT campaign_interaction_states_check1 CHECK (((((acceptance_provenance)::text = 'verified'::text) AND (accepted_revision_id IS NOT NULL) AND (accepted_publication_version IS NOT NULL) AND (accepted_publication_version > 0) AND (accepted_terms_hash IS NOT NULL) AND ((accepted_terms_hash)::text ~ '^[0-9a-f]{64}$'::text) AND (accepted_at IS NOT NULL)) OR (((acceptance_provenance)::text = 'neverAccepted'::text) AND ((status)::text = ANY ((ARRAY['left'::character varying, 'removed'::character varying])::text[])) AND (accepted_revision_id IS NULL) AND (accepted_publication_version IS NULL) AND (accepted_terms_hash IS NULL) AND (accepted_at IS NULL)) OR (((acceptance_provenance)::text = 'legacyUnknown'::text) AND (accepted_revision_id IS NULL) AND (accepted_publication_version IS NULL) AND (accepted_terms_hash IS NULL)))),
    CONSTRAINT campaign_interaction_states_kind_check CHECK (((kind)::text = ANY ((ARRAY['join'::character varying, 'reminder'::character varying])::text[]))),
    CONSTRAINT campaign_interaction_states_row_version_check CHECK ((row_version > 0)),
    CONSTRAINT campaign_interaction_states_wallet_address_check CHECK (((wallet_address)::text ~ '^0x[0-9a-f]{40}$'::text))
);

CREATE TABLE public.campaign_legacy_activations (
    id uuid NOT NULL,
    campaign_id text NOT NULL,
    run_id uuid NOT NULL,
    decision character varying(24) NOT NULL,
    review_id uuid,
    command_id uuid NOT NULL,
    publish_command_id uuid,
    revision_id uuid NOT NULL,
    activated_by character varying(42) NOT NULL,
    activated_at timestamp(6) with time zone NOT NULL,
    legacy_row_sha256 character varying(64) NOT NULL,
    interactions_sha256 character varying(64) NOT NULL,
    imported_interactions integer NOT NULL,
    comparison jsonb NOT NULL,
    CONSTRAINT campaign_legacy_activation_shape CHECK ((((decision)::text = ANY ((ARRAY['import-public'::character varying, 'import-unpublished'::character varying])::text[])) AND (((decision)::text = 'import-public'::text) = (publish_command_id IS NOT NULL)) AND ((activated_by)::text ~ '^0x[0-9a-f]{40}$'::text) AND ((legacy_row_sha256)::text ~ '^[0-9a-f]{64}$'::text) AND ((interactions_sha256)::text ~ '^[0-9a-f]{64}$'::text) AND (imported_interactions >= 0) AND (jsonb_typeof(comparison) = 'object'::text)))
);

CREATE TABLE public.campaign_legacy_archive_rows (
    run_id uuid NOT NULL,
    source_table character varying(32) NOT NULL,
    row_id text NOT NULL,
    campaign_id text NOT NULL,
    row_json text NOT NULL,
    row_sha256 character varying(64) NOT NULL,
    CONSTRAINT campaign_legacy_archive_rows_row_sha256_check CHECK (((row_sha256)::text ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT campaign_legacy_archive_rows_source_table_check CHECK (((source_table)::text = ANY ((ARRAY['launchpad_campaigns'::character varying, 'campaign_participants'::character varying, 'campaign_reminders'::character varying])::text[])))
);

CREATE TABLE public.campaign_legacy_fence_clock (
    id smallint NOT NULL,
    ticks bigint NOT NULL,
    CONSTRAINT campaign_legacy_fence_clock_id_check CHECK ((id = 1)),
    CONSTRAINT campaign_legacy_fence_clock_ticks_check CHECK ((ticks >= 0))
);

CREATE TABLE public.campaign_legacy_import_items (
    run_id uuid NOT NULL,
    campaign_id text NOT NULL,
    legacy_row_sha256 character varying(64) NOT NULL,
    interactions_sha256 character varying(64) NOT NULL,
    participant_rows integer NOT NULL,
    reminder_rows integer NOT NULL,
    decision character varying(24) NOT NULL,
    findings jsonb NOT NULL,
    CONSTRAINT campaign_legacy_import_items_decision_check CHECK (((decision)::text = ANY ((ARRAY['import-public'::character varying, 'import-unpublished'::character varying, 'review-required'::character varying, 'blocked'::character varying, 'already-versioned'::character varying])::text[]))),
    CONSTRAINT campaign_legacy_import_items_findings_check CHECK ((jsonb_typeof(findings) = 'array'::text)),
    CONSTRAINT campaign_legacy_import_items_interactions_sha256_check CHECK (((interactions_sha256)::text ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT campaign_legacy_import_items_legacy_row_sha256_check CHECK (((legacy_row_sha256)::text ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT campaign_legacy_import_items_participant_rows_check CHECK ((participant_rows >= 0)),
    CONSTRAINT campaign_legacy_import_items_reminder_rows_check CHECK ((reminder_rows >= 0))
);

CREATE TABLE public.campaign_legacy_import_runs (
    id uuid NOT NULL,
    algorithm character varying(32) NOT NULL,
    status character varying(16) DEFAULT 'capturing'::character varying NOT NULL,
    operator character varying(96) NOT NULL,
    limits jsonb NOT NULL,
    limit_hit character varying(32),
    snapshot text,
    captured_at timestamp(6) with time zone,
    source_sha256 character varying(64),
    row_count bigint,
    byte_count bigint,
    retain_until timestamp(6) with time zone NOT NULL,
    created_at timestamp(6) with time zone DEFAULT clock_timestamp() NOT NULL,
    finished_at timestamp(6) with time zone,
    abandoned_by character varying(96),
    CONSTRAINT campaign_legacy_run_shape CHECK ((((operator)::text ~ '^[A-Za-z0-9._@:-]{1,96}$'::text) AND (jsonb_typeof(limits) = 'object'::text) AND (retain_until > created_at) AND (((status)::text <> 'captured'::text) OR ((snapshot IS NOT NULL) AND (captured_at IS NOT NULL) AND ((source_sha256)::text ~ '^[0-9a-f]{64}$'::text) AND (row_count >= 0) AND (byte_count >= 0) AND (finished_at IS NOT NULL) AND (limit_hit IS NULL))) AND (((status)::text <> 'incomplete'::text) OR ((limit_hit IS NOT NULL) AND (finished_at IS NOT NULL))) AND (((status)::text = 'abandoned'::text) = (abandoned_by IS NOT NULL)) AND ((abandoned_by IS NULL) OR ((abandoned_by)::text ~ '^[A-Za-z0-9._@:-]{1,96}$'::text)))),
    CONSTRAINT campaign_legacy_run_status CHECK (((status)::text = ANY ((ARRAY['capturing'::character varying, 'captured'::character varying, 'incomplete'::character varying, 'abandoned'::character varying])::text[])))
);

CREATE TABLE public.campaign_legacy_review_decisions (
    id uuid NOT NULL,
    run_id uuid NOT NULL,
    campaign_id text NOT NULL,
    decision character varying(24) NOT NULL,
    accepted_differences jsonb NOT NULL,
    acknowledged_findings jsonb NOT NULL,
    reason character varying(1000) NOT NULL,
    decided_by character varying(42) NOT NULL,
    client_command_id uuid NOT NULL,
    decided_at timestamp(6) with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT campaign_legacy_review_shape CHECK ((((decision)::text = ANY ((ARRAY['import-public'::character varying, 'import-unpublished'::character varying, 'exclude'::character varying])::text[])) AND ((decided_by)::text ~ '^0x[0-9a-f]{40}$'::text) AND (length(btrim((reason)::text)) > 0) AND (jsonb_typeof(accepted_differences) = 'array'::text) AND (accepted_differences <@ '["visibility", "participantCount", "walletState"]'::jsonb) AND (jsonb_typeof(acknowledged_findings) = 'array'::text)))
);

CREATE TABLE public.campaign_legacy_write_fences (
    campaign_id text NOT NULL,
    run_id uuid NOT NULL,
    fenced_by character varying(96) NOT NULL,
    fenced_at timestamp(6) with time zone DEFAULT clock_timestamp() NOT NULL,
    lifted_by character varying(96),
    lifted_at timestamp(6) with time zone,
    CONSTRAINT campaign_legacy_fence_shape CHECK ((((fenced_by)::text ~ '^[A-Za-z0-9._@:-]{1,96}$'::text) AND (((lifted_by IS NULL) AND (lifted_at IS NULL)) OR (((lifted_by)::text ~ '^[A-Za-z0-9._@:-]{1,96}$'::text) AND (lifted_at IS NOT NULL)))))
);

CREATE TABLE public.durable_outbox (
    id uuid NOT NULL,
    source character varying(16) NOT NULL,
    aggregate_id character varying(500) NOT NULL,
    aggregate_version bigint NOT NULL,
    topic character varying(100) NOT NULL,
    owner_scope character varying(42),
    payload_version integer NOT NULL,
    payload jsonb NOT NULL,
    occurred_at timestamp(6) with time zone NOT NULL,
    available_at timestamp(6) with time zone NOT NULL,
    published_at timestamp(6) with time zone,
    lease_owner uuid,
    lease_until timestamp(6) with time zone,
    attempts integer DEFAULT 0 NOT NULL,
    error_code character varying(100),
    dead_lettered_at timestamp(6) with time zone,
    replay_generation bigint DEFAULT 0 NOT NULL,
    replay_transaction character varying(20),
    CONSTRAINT durable_outbox_aggregate_version_check CHECK ((aggregate_version > 0)),
    CONSTRAINT durable_outbox_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT durable_outbox_owner_scope_check CHECK (((owner_scope IS NULL) OR ((owner_scope)::text ~ '^0x[0-9a-f]{40}$'::text))),
    CONSTRAINT durable_outbox_payload_version_check CHECK ((payload_version > 0)),
    CONSTRAINT durable_outbox_replay_generation_check CHECK ((replay_generation >= 0)),
    CONSTRAINT durable_outbox_source_check CHECK (((source)::text = ANY ((ARRAY['campaign'::character varying, 'chain'::character varying])::text[])))
);

CREATE VIEW public.campaign_outbox_dead_letters WITH (security_barrier='true') AS
 SELECT durable_outbox.id AS event_id,
        CASE
            WHEN ((durable_outbox.topic)::text = ANY ((ARRAY['campaign.publication.changed'::character varying, 'campaign.interaction.changed'::character varying, 'campaign.personal.changed'::character varying])::text[])) THEN durable_outbox.topic
            ELSE 'unsupported'::character varying
        END AS topic,
    durable_outbox.aggregate_version AS stream_version,
    encode(sha256(convert_to((durable_outbox.aggregate_id)::text, 'UTF8'::name)), 'hex'::text) AS stream_digest,
    durable_outbox.occurred_at,
    durable_outbox.replay_generation,
    durable_outbox.attempts,
    durable_outbox.dead_lettered_at,
        CASE
            WHEN ((durable_outbox.error_code)::text = ANY ((ARRAY['publisherUnavailable'::character varying, 'unsupportedSchema'::character varying, 'leaseExpired'::character varying])::text[])) THEN durable_outbox.error_code
            ELSE NULL::character varying
        END AS error_code,
    durable_outbox.published_at,
    durable_outbox.available_at,
    COALESCE((durable_outbox.lease_until > clock_timestamp()), false) AS lease_active
   FROM public.durable_outbox
  WHERE (((durable_outbox.source)::text = 'campaign'::text) AND (durable_outbox.published_at IS NULL) AND (durable_outbox.dead_lettered_at IS NOT NULL));

CREATE TABLE public.campaign_participants (
    id text NOT NULL,
    campaign_id text NOT NULL,
    wallet_address character varying(42) NOT NULL,
    joined_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);

CREATE TABLE public.campaign_private_scopes (
    wallet_address character varying(42) NOT NULL,
    scope_id uuid NOT NULL,
    CONSTRAINT campaign_private_scopes_wallet_address_check CHECK (((wallet_address)::text ~ '^0x[0-9a-f]{40}$'::text))
);

CREATE TABLE public.campaign_publication_history (
    campaign_id text NOT NULL,
    publication_version bigint NOT NULL,
    command_id uuid NOT NULL,
    actor character varying(42) NOT NULL,
    before_mode character varying(16) NOT NULL,
    after_mode character varying(16) NOT NULL,
    before_revision_id uuid,
    after_revision_id uuid,
    occurred_at timestamp(6) with time zone NOT NULL,
    CONSTRAINT campaign_publication_history_publication_version_check CHECK ((publication_version > 0))
);

CREATE TABLE public.campaign_publications (
    campaign_id text NOT NULL,
    publication_version bigint DEFAULT 0 NOT NULL,
    interaction_version bigint DEFAULT 0 NOT NULL,
    active_revision_id uuid,
    mode character varying(16) DEFAULT 'enabled'::character varying NOT NULL,
    updated_by character varying(96) NOT NULL,
    updated_at timestamp(6) with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT campaign_publications_check CHECK (((active_revision_id IS NULL) OR (publication_version > 0))),
    CONSTRAINT campaign_publications_interaction_version_check CHECK ((interaction_version >= 0)),
    CONSTRAINT campaign_publications_mode_check CHECK (((mode)::text = ANY ((ARRAY['enabled'::character varying, 'paused'::character varying, 'archived'::character varying])::text[]))),
    CONSTRAINT campaign_publications_publication_version_check CHECK ((publication_version >= 0))
);

CREATE TABLE public.campaign_reminders (
    id text NOT NULL,
    campaign_id text NOT NULL,
    wallet_address character varying(42) NOT NULL,
    created_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);

CREATE TABLE public.campaign_revisions (
    id uuid NOT NULL,
    campaign_id text NOT NULL,
    schema_version integer NOT NULL,
    terms_canonical_version integer NOT NULL,
    input jsonb NOT NULL,
    material_terms_hash character varying(64) NOT NULL,
    content_hash character varying(64) NOT NULL,
    created_by character varying(96) NOT NULL,
    created_at timestamp(6) with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT campaign_revision_shape CHECK (((schema_version > 0) AND (terms_canonical_version > 0) AND (jsonb_typeof(input) = 'object'::text) AND ((material_terms_hash)::text ~ '^[0-9a-f]{64}$'::text) AND ((content_hash)::text ~ '^[0-9a-f]{64}$'::text)))
);

CREATE TABLE public.categories (
    id text NOT NULL,
    name text NOT NULL,
    description text,
    slug text,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.comments (
    id text NOT NULL,
    content text NOT NULL,
    "authorId" text NOT NULL,
    "articleId" text NOT NULL,
    "parentId" text,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.connected_sites (
    id text NOT NULL,
    "ownerAddress" character varying(42) NOT NULL,
    "chainId" integer,
    origin character varying(180) NOT NULL,
    domain character varying(120) NOT NULL,
    "siteName" character varying(120) NOT NULL,
    "iconUrl" text,
    category character varying(60),
    "riskLevel" character varying(20) DEFAULT 'unknown'::character varying NOT NULL,
    permissions jsonb DEFAULT '[]'::jsonb NOT NULL,
    "firstConnectedAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "lastConnectedAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.consumer_receipts (
    consumer_id character varying(100) NOT NULL,
    event_id uuid NOT NULL,
    consumed_at timestamp(6) with time zone NOT NULL
);

CREATE TABLE public.conversations (
    id text NOT NULL,
    title text NOT NULL,
    "userId" text NOT NULL,
    model text DEFAULT 'gpt-3.5-turbo'::text NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL,
    "modelProvider" text,
    "modelVersion" text,
    "messageCount" integer DEFAULT 0,
    "totalTokens" integer DEFAULT 0,
    "inputTokens" integer DEFAULT 0,
    "outputTokens" integer DEFAULT 0,
    "totalCost" numeric(65,30) DEFAULT 0,
    status public."ConversationStatus" DEFAULT 'ACTIVE'::public."ConversationStatus",
    temperature double precision DEFAULT 0.7,
    "maxTokens" integer DEFAULT 1000,
    "lastMessageAt" timestamp(3) without time zone,
    duration integer,
    category text,
    tags text[] DEFAULT ARRAY[]::text[],
    "userRating" integer,
    feedback text
);

CREATE TABLE public.execution_registry_activations (
    activation_version bigint NOT NULL,
    revision bigint NOT NULL,
    previous_revision bigint,
    activated_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    activated_by text NOT NULL,
    reason text NOT NULL,
    code_observations jsonb NOT NULL,
    xid xid8 DEFAULT pg_current_xact_id() NOT NULL,
    CONSTRAINT execution_registry_activations_values_check CHECK (((activation_version > 0) AND ((length(activated_by) >= 1) AND (length(activated_by) <= 128)) AND ((length(reason) >= 1) AND (length(reason) <= 500))))
);

CREATE TABLE public.execution_registry_bindings (
    revision bigint NOT NULL,
    chain_id integer NOT NULL,
    operation text NOT NULL,
    market text NOT NULL,
    status text NOT NULL,
    interface_id text NOT NULL,
    target_name text NOT NULL,
    roles jsonb NOT NULL,
    binding_digest text NOT NULL,
    CONSTRAINT execution_registry_bindings_exit_check CHECK ((NOT ((operation = 'trade.closePosition'::text) AND (status = 'disabled'::text)))),
    CONSTRAINT execution_registry_bindings_values_check CHECK (((operation = ANY (ARRAY['trade.approveCollateral'::text, 'trade.openPosition'::text, 'trade.closePosition'::text])) AND (status = ANY (ARRAY['enabled'::text, 'disabled'::text])) AND (binding_digest ~ '^[0-9a-f]{64}$'::text) AND (interface_id ~ '^[a-z][a-z0-9]*(\.[a-zA-Z][a-zA-Z0-9]*)+\.v[1-9][0-9]*$'::text)))
);

CREATE TABLE public.execution_registry_entries (
    revision bigint NOT NULL,
    chain_id integer NOT NULL,
    name text NOT NULL,
    address text NOT NULL,
    deployment_file text NOT NULL,
    deployment_timestamp text NOT NULL,
    abi_hash text NOT NULL,
    interfaces text[] NOT NULL,
    code_hash text NOT NULL,
    code_block_number bigint NOT NULL,
    code_block_hash text NOT NULL,
    entry_digest text NOT NULL,
    CONSTRAINT execution_registry_entries_values_check CHECK (((chain_id > 0) AND (name ~ '^[A-Za-z][A-Za-z0-9]{0,63}$'::text) AND (address ~ '^0x[0-9a-f]{40}$'::text) AND (address <> '0x0000000000000000000000000000000000000000'::text) AND ((length(deployment_file) >= 1) AND (length(deployment_file) <= 128)) AND ((length(deployment_timestamp) >= 1) AND (length(deployment_timestamp) <= 64)) AND (abi_hash ~ '^[0-9a-f]{64}$'::text) AND (entry_digest ~ '^[0-9a-f]{64}$'::text) AND (code_hash ~ '^0x[0-9a-f]{64}$'::text) AND (code_hash <> '0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470'::text) AND (code_block_number >= 0) AND (code_block_hash ~ '^0x[0-9a-f]{64}$'::text)))
);

CREATE TABLE public.execution_registry_markets (
    revision bigint NOT NULL,
    chain_id integer NOT NULL,
    symbol text NOT NULL,
    position_manager text NOT NULL,
    index_token text NOT NULL,
    collateral_token text NOT NULL,
    index_decimals smallint NOT NULL,
    collateral_decimals smallint NOT NULL,
    price_precision smallint NOT NULL,
    components text[] NOT NULL,
    market_digest text NOT NULL,
    CONSTRAINT execution_registry_markets_values_check CHECK (((symbol ~ '^[A-Z0-9]{1,16}-[A-Z0-9]{1,16}$'::text) AND ((index_decimals >= 0) AND (index_decimals <= 36)) AND ((collateral_decimals >= 0) AND (collateral_decimals <= 30)) AND ((price_precision >= 0) AND (price_precision <= 36)) AND (market_digest ~ '^[0-9a-f]{64}$'::text)))
);

CREATE TABLE public.execution_registry_pointer (
    id smallint NOT NULL,
    active_revision bigint NOT NULL,
    activation_version bigint NOT NULL,
    CONSTRAINT execution_registry_pointer_singleton_check CHECK (((id = 1) AND (activation_version > 0)))
);

CREATE TABLE public.execution_registry_snapshots (
    revision bigint NOT NULL,
    schema_version integer NOT NULL,
    manifest_digest text NOT NULL,
    declarations_digest text NOT NULL,
    content_digest text,
    sealed boolean DEFAULT false NOT NULL,
    builder_xid xid8 DEFAULT pg_current_xact_id() NOT NULL,
    source_summary jsonb NOT NULL,
    staged_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    staged_by text NOT NULL,
    reason text NOT NULL,
    CONSTRAINT execution_registry_snapshots_actor_check CHECK ((((length(staged_by) >= 1) AND (length(staged_by) <= 128)) AND ((length(reason) >= 1) AND (length(reason) <= 500)))),
    CONSTRAINT execution_registry_snapshots_digests_check CHECK (((manifest_digest ~ '^[0-9a-f]{64}$'::text) AND (declarations_digest ~ '^[0-9a-f]{64}$'::text) AND ((content_digest IS NULL) OR (content_digest ~ '^[0-9a-f]{64}$'::text)))),
    CONSTRAINT execution_registry_snapshots_revision_check CHECK ((revision > 0)),
    CONSTRAINT execution_registry_snapshots_schema_check CHECK ((schema_version = 1)),
    CONSTRAINT execution_registry_snapshots_seal_check CHECK ((sealed = (content_digest IS NOT NULL)))
);

CREATE SEQUENCE public.execution_registry_revision_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.execution_registry_revision_seq OWNED BY public.execution_registry_snapshots.revision;

CREATE TABLE public.hidden_assets (
    id text NOT NULL,
    "ownerAddress" character varying(42) NOT NULL,
    "walletAddress" character varying(42),
    "chainId" integer NOT NULL,
    "assetKey" character varying(180) NOT NULL,
    "assetAddress" character varying(42),
    symbol character varying(32) NOT NULL,
    reason character varying(40) DEFAULT 'manual'::character varying NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.i18n_audit_logs (
    id text NOT NULL,
    actor character varying(42) NOT NULL,
    action character varying(40) NOT NULL,
    locale_code character varying(35),
    namespace character varying(120),
    message_key character varying(240),
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);

CREATE TABLE public.i18n_catalog_revisions (
    id text NOT NULL,
    locale_code character varying(35) NOT NULL,
    version integer NOT NULL,
    catalog jsonb NOT NULL,
    checksum character varying(64) NOT NULL,
    published_by character varying(42) NOT NULL,
    published_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    source_revision_id text
);

CREATE TABLE public.i18n_locales (
    code character varying(35) NOT NULL,
    english_name character varying(80) NOT NULL,
    native_name character varying(80) NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    is_default boolean DEFAULT false NOT NULL,
    created_by character varying(42),
    updated_by character varying(42),
    created_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.i18n_message_drafts (
    id text NOT NULL,
    locale_code character varying(35) NOT NULL,
    namespace character varying(120) NOT NULL,
    message_key character varying(240) NOT NULL,
    value text NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    updated_by character varying(42),
    created_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.interview_rooms (
    id text NOT NULL,
    "roomName" text NOT NULL,
    "displayName" text NOT NULL,
    "roomType" public."RoomType" DEFAULT 'PANEL'::public."RoomType" NOT NULL,
    status public."RoomStatus" DEFAULT 'ACTIVE'::public."RoomStatus" NOT NULL,
    "maxParticipants" integer DEFAULT 6 NOT NULL,
    "isPublic" boolean DEFAULT true NOT NULL,
    password text,
    description text,
    tags text[] DEFAULT ARRAY[]::text[],
    "creatorId" text,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL,
    "startedAt" timestamp(3) without time zone,
    "endedAt" timestamp(3) without time zone,
    "totalDuration" integer DEFAULT 0 NOT NULL,
    "peakParticipants" integer,
    "totalJoins" integer DEFAULT 0 NOT NULL
);

CREATE TABLE public.launchpad_campaigns (
    id text NOT NULL,
    title character varying(200) NOT NULL,
    description text,
    banner text,
    start_date timestamp(3) without time zone NOT NULL,
    end_date timestamp(3) without time zone NOT NULL,
    status text DEFAULT 'upcoming'::text NOT NULL,
    reward text,
    participants integer DEFAULT 0 NOT NULL,
    metadata jsonb,
    created_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.lowcode_pages (
    id text NOT NULL,
    name text NOT NULL,
    slug text NOT NULL,
    content jsonb NOT NULL,
    description text,
    "isHomePage" boolean DEFAULT false NOT NULL,
    published boolean DEFAULT false NOT NULL,
    config jsonb,
    meta jsonb,
    "ownerId" text NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL,
    "publishedAt" timestamp(3) without time zone
);

CREATE TABLE public.market_candles (
    id text NOT NULL,
    source character varying(32) NOT NULL,
    symbol character varying(32) NOT NULL,
    resolution character varying(8) NOT NULL,
    bucket_start timestamp(3) without time zone NOT NULL,
    open numeric(38,18) NOT NULL,
    high numeric(38,18) NOT NULL,
    low numeric(38,18) NOT NULL,
    close numeric(38,18) NOT NULL,
    volume numeric(38,18) NOT NULL,
    quote_volume numeric(38,18) NOT NULL,
    closed boolean DEFAULT true NOT NULL,
    ingested_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);

CREATE TABLE public.messages (
    id text NOT NULL,
    "conversationId" text NOT NULL,
    role text NOT NULL,
    content text NOT NULL,
    model text,
    "tokenCount" integer,
    "userId" text,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "roleEnum" public."MessageRole",
    "modelProvider" text,
    "inputTokens" integer,
    "outputTokens" integer,
    "totalTokens" integer,
    "responseTime" integer,
    cost numeric(65,30),
    confidence double precision,
    "userRating" integer,
    temperature double precision,
    "maxTokens" integer,
    "finishReason" text,
    "readAt" timestamp(3) without time zone
);

CREATE TABLE public.mobile_docs (
    id text NOT NULL,
    title text NOT NULL,
    content text NOT NULL,
    summary text,
    "filePath" text,
    category public."DocCategory" DEFAULT 'FRONTEND'::public."DocCategory" NOT NULL,
    tags text[] DEFAULT ARRAY[]::text[],
    "isHot" boolean DEFAULT false NOT NULL,
    "sortOrder" integer DEFAULT 0 NOT NULL,
    published boolean DEFAULT true NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.model_statistics (
    id text NOT NULL,
    model text NOT NULL,
    "modelProvider" text NOT NULL,
    date timestamp(3) without time zone NOT NULL,
    "requestCount" integer DEFAULT 0 NOT NULL,
    "userCount" integer DEFAULT 0 NOT NULL,
    "totalTokens" integer DEFAULT 0 NOT NULL,
    "totalCost" numeric(65,30) DEFAULT 0 NOT NULL,
    "avgResponseTime" integer,
    "maxResponseTime" integer,
    "minResponseTime" integer,
    "avgRating" double precision,
    "errorRate" double precision,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.notification_prefs (
    id text NOT NULL,
    "ownerAddress" character varying(42) NOT NULL,
    "securityAlerts" boolean DEFAULT true NOT NULL,
    "txUpdates" boolean DEFAULT true NOT NULL,
    "marketAlerts" boolean DEFAULT true NOT NULL,
    "productUpdates" boolean DEFAULT false NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.oracle_price_observations (
    id text NOT NULL,
    source character varying(32) NOT NULL,
    symbol character varying(32) NOT NULL,
    chain_id integer NOT NULL,
    feed_address character varying(42) NOT NULL,
    round_id character varying(80) NOT NULL,
    price numeric(38,18) NOT NULL,
    decimals integer NOT NULL,
    observed_at timestamp(3) without time zone NOT NULL,
    recorded_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);

CREATE TABLE public.outbox_replay_intents (
    command_id uuid NOT NULL,
    principal character varying(128) NOT NULL,
    authority_role character varying(128) NOT NULL,
    authority_version bigint NOT NULL,
    operation character varying(32) NOT NULL,
    source character varying(16) NOT NULL,
    event_id uuid NOT NULL,
    expected_generation bigint NOT NULL,
    expected_dead_lettered_at timestamp(6) with time zone NOT NULL,
    expected_attempts integer NOT NULL,
    expected_error_code character varying(100) NOT NULL,
    fingerprint character varying(64) NOT NULL,
    reason character varying(500) NOT NULL,
    request_id character varying(128) NOT NULL,
    accepted_at timestamp(6) with time zone DEFAULT clock_timestamp() NOT NULL,
    schema_version integer DEFAULT 1 NOT NULL,
    CONSTRAINT outbox_replay_intents_authority_role_check CHECK (((authority_role)::text ~ '^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$'::text)),
    CONSTRAINT outbox_replay_intents_authority_version_check CHECK ((authority_version > 0)),
    CONSTRAINT outbox_replay_intents_expected_attempts_check CHECK ((expected_attempts > 0)),
    CONSTRAINT outbox_replay_intents_expected_error_code_check CHECK (((expected_error_code)::text = ANY ((ARRAY['publisherUnavailable'::character varying, 'unsupportedSchema'::character varying, 'leaseExpired'::character varying])::text[]))),
    CONSTRAINT outbox_replay_intents_expected_generation_check CHECK (((expected_generation >= 0) AND (expected_generation < '9223372036854775807'::bigint))),
    CONSTRAINT outbox_replay_intents_fingerprint_check CHECK (((fingerprint)::text ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT outbox_replay_intents_operation_check CHECK (((operation)::text = 'campaign.outbox.replay'::text)),
    CONSTRAINT outbox_replay_intents_principal_check CHECK (((principal)::text ~ '^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$'::text)),
    CONSTRAINT outbox_replay_intents_reason_check CHECK ((((octet_length((reason)::text) >= 1) AND (octet_length((reason)::text) <= 500)) AND ((reason)::text !~ '[[:cntrl:]]'::text) AND ((reason)::text !~ '^[[:space:]]|[[:space:]]$'::text))),
    CONSTRAINT outbox_replay_intents_request_id_check CHECK (((request_id)::text ~ '^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$'::text)),
    CONSTRAINT outbox_replay_intents_schema_version_check CHECK ((schema_version = 1)),
    CONSTRAINT outbox_replay_intents_source_check CHECK (((source)::text = 'campaign'::text))
);

CREATE TABLE public.outbox_replay_outcomes (
    command_id uuid NOT NULL,
    event_id uuid NOT NULL,
    replay_generation bigint NOT NULL,
    status character varying(16) NOT NULL,
    code character varying(32) NOT NULL,
    before_state jsonb NOT NULL,
    after_state jsonb NOT NULL,
    completed_at timestamp(6) with time zone NOT NULL,
    write_transaction character varying(20) NOT NULL,
    schema_version integer DEFAULT 1 NOT NULL,
    CONSTRAINT outbox_replay_outcomes_check CHECK (((((status)::text = 'succeeded'::text) AND ((code)::text = 'replayed'::text)) OR (((status)::text = 'refused'::text) AND ((code)::text = 'stateChanged'::text)))),
    CONSTRAINT outbox_replay_outcomes_replay_generation_check CHECK ((replay_generation >= 0)),
    CONSTRAINT outbox_replay_outcomes_schema_version_check CHECK ((schema_version = 1))
);

CREATE TABLE public.outbox_streams (
    stream_key character varying(500) NOT NULL,
    source character varying(16) NOT NULL,
    last_stream_version bigint NOT NULL,
    CONSTRAINT outbox_streams_last_stream_version_check CHECK ((last_stream_version > 0)),
    CONSTRAINT outbox_streams_source_check CHECK (((source)::text = ANY ((ARRAY['campaign'::character varying, 'chain'::character varying])::text[])))
);

CREATE TABLE public.perp_orders (
    id text NOT NULL,
    chain_id integer DEFAULT 11155111 NOT NULL,
    account text NOT NULL,
    token text NOT NULL,
    "isLong" boolean NOT NULL,
    "sizeDelta" text NOT NULL,
    "triggerPrice" text,
    type text NOT NULL,
    status text NOT NULL,
    "txHash" text,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.perp_positions (
    id text NOT NULL,
    chain_id integer DEFAULT 11155111 NOT NULL,
    account text NOT NULL,
    token text NOT NULL,
    "isLong" boolean NOT NULL,
    size text NOT NULL,
    collateral text NOT NULL,
    "entryPrice" text NOT NULL,
    "markPrice" text NOT NULL,
    pnl text NOT NULL,
    status text NOT NULL,
    "txHash" text NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.perp_trades (
    id text NOT NULL,
    chain_id integer DEFAULT 11155111 NOT NULL,
    account text NOT NULL,
    token text NOT NULL,
    "isLong" boolean NOT NULL,
    "sizeDelta" text NOT NULL,
    price text NOT NULL,
    fee text NOT NULL,
    pnl text,
    type text NOT NULL,
    "txHash" text NOT NULL,
    log_index integer,
    "blockNumber" integer NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    collateral_delta text DEFAULT '0'::text NOT NULL
);

CREATE TABLE public.profile_follows (
    id text NOT NULL,
    follower_address character varying(42) NOT NULL,
    following_address character varying(42) NOT NULL,
    created_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);

CREATE TABLE public.room_participants (
    id text NOT NULL,
    "roomId" text NOT NULL,
    "userId" text,
    "displayName" text NOT NULL,
    identity text NOT NULL,
    role public."ParticipantRole" DEFAULT 'OBSERVER'::public."ParticipantRole" NOT NULL,
    "joinedAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "leftAt" timestamp(3) without time zone,
    duration integer DEFAULT 0 NOT NULL,
    "isActive" boolean DEFAULT true NOT NULL,
    "connectionQuality" public."ConnectionQuality" DEFAULT 'UNKNOWN'::public."ConnectionQuality" NOT NULL
);

CREATE TABLE public.room_sessions (
    id text NOT NULL,
    "roomId" text NOT NULL,
    "startedAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "endedAt" timestamp(3) without time zone,
    duration integer DEFAULT 0 NOT NULL,
    "participantCount" integer
);

CREATE TABLE public.staking_actions (
    id text NOT NULL,
    chain_id integer NOT NULL,
    pool_id text,
    pool_address character varying(42) NOT NULL,
    user_address character varying(42) NOT NULL,
    action text NOT NULL,
    amount_raw text NOT NULL,
    tx_hash text NOT NULL,
    log_index integer NOT NULL,
    block_number bigint NOT NULL,
    created_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);

CREATE TABLE public.staking_pools (
    id text NOT NULL,
    name character varying(100) NOT NULL,
    chain_id integer NOT NULL,
    pool_type text NOT NULL,
    token_address character varying(42) NOT NULL,
    reward_token character varying(42),
    apy double precision,
    tvl double precision,
    status text DEFAULT 'active'::text NOT NULL,
    starts_at timestamp(3) without time zone,
    ends_at timestamp(3) without time zone,
    metadata jsonb,
    created_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.tags (
    id text NOT NULL,
    name text NOT NULL,
    slug text,
    description text,
    color text,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.token_candles (
    id text NOT NULL,
    "tokenId" text NOT NULL,
    "time" timestamp(3) without time zone NOT NULL,
    resolution text NOT NULL,
    open numeric(38,18) NOT NULL,
    high numeric(38,18) NOT NULL,
    low numeric(38,18) NOT NULL,
    close numeric(38,18) NOT NULL,
    volume numeric(38,18) NOT NULL
);

CREATE TABLE public.token_comments (
    id text NOT NULL,
    content text NOT NULL,
    "tokenId" text NOT NULL,
    "userId" text NOT NULL,
    likes integer DEFAULT 0 NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.token_holders (
    id text NOT NULL,
    "tokenId" text NOT NULL,
    user_address character varying(42) NOT NULL,
    balance text NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.token_trades (
    id text NOT NULL,
    "tokenId" text NOT NULL,
    user_address character varying(42) NOT NULL,
    type text NOT NULL,
    "tokenAmount" text NOT NULL,
    "ethAmount" text NOT NULL,
    price numeric(38,18) NOT NULL,
    block_number bigint NOT NULL,
    transaction_hash text NOT NULL,
    "timestamp" timestamp(3) without time zone NOT NULL,
    chain_id integer DEFAULT 11155111 NOT NULL,
    log_index integer
);

CREATE TABLE public.tokens (
    id text NOT NULL,
    address character varying(42),
    chain_id integer NOT NULL,
    symbol character varying(20) NOT NULL,
    name character varying(100) NOT NULL,
    description text,
    image text,
    banner text,
    tags jsonb DEFAULT '[]'::jsonb NOT NULL,
    twitter text,
    discord text,
    telegram text,
    website text,
    whitepaper text,
    status public."TokenStatus" DEFAULT 'PENDING'::public."TokenStatus" NOT NULL,
    launch_type public."LaunchType" DEFAULT 'NEW_COIN'::public."LaunchType" NOT NULL,
    pre_buy_percent double precision,
    has_margin boolean DEFAULT false NOT NULL,
    has_reservation boolean DEFAULT false NOT NULL,
    is_official boolean DEFAULT false NOT NULL,
    custom_address character varying(4),
    bonding_curve character varying(42),
    market_cap numeric(38,18),
    volume_24h numeric(38,18),
    price_change_24h double precision,
    creator_address character varying(42) NOT NULL,
    launched_at timestamp(3) without time zone,
    created_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.user_preferences (
    id text NOT NULL,
    "ownerAddress" character varying(42) NOT NULL,
    theme character varying(20) DEFAULT 'system'::character varying NOT NULL,
    "fiatCurrency" character varying(8) DEFAULT 'USD'::character varying NOT NULL,
    "testnetEnabled" boolean DEFAULT true NOT NULL,
    "notificationsEnabled" boolean DEFAULT true NOT NULL,
    "spamFilterLevel" character varying(20) DEFAULT 'standard'::character varying NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.user_stakes (
    id text NOT NULL,
    user_address character varying(42) NOT NULL,
    pool_id text NOT NULL,
    amount numeric(36,18) NOT NULL,
    rewards_claimed numeric(36,18) DEFAULT 0 NOT NULL,
    staked_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    unstaked_at timestamp(3) without time zone
);

CREATE TABLE public.user_statistics (
    id text NOT NULL,
    "userId" text NOT NULL,
    date timestamp(3) without time zone NOT NULL,
    "conversationCount" integer DEFAULT 0 NOT NULL,
    "messageCount" integer DEFAULT 0 NOT NULL,
    "totalTokens" integer DEFAULT 0 NOT NULL,
    "totalCost" numeric(65,30) DEFAULT 0 NOT NULL,
    "totalDuration" integer DEFAULT 0 NOT NULL,
    "avgResponseTime" integer,
    "modelUsage" jsonb DEFAULT '{}'::jsonb NOT NULL,
    "avgRating" double precision,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.users (
    id text NOT NULL,
    email text NOT NULL,
    username text NOT NULL,
    "fullName" text,
    password text NOT NULL,
    avatar text,
    bio text,
    roles jsonb DEFAULT '["user"]'::jsonb NOT NULL,
    "refreshToken" text,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.verification_codes (
    id text NOT NULL,
    code text NOT NULL,
    email text NOT NULL,
    type public."VerificationCodeType" NOT NULL,
    status public."VerificationCodeStatus" DEFAULT 'PENDING'::public."VerificationCodeStatus" NOT NULL,
    "userId" text,
    "expiresAt" timestamp(3) without time zone NOT NULL,
    "verifiedAt" timestamp(3) without time zone,
    "revokedAt" timestamp(3) without time zone,
    attempts integer DEFAULT 0 NOT NULL,
    "maxAttempts" integer DEFAULT 3 NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.wallet_groups (
    id text NOT NULL,
    "ownerAddress" character varying(42) NOT NULL,
    name character varying(80) NOT NULL,
    color character varying(24),
    "sortOrder" integer DEFAULT 0 NOT NULL,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.wallet_profiles (
    id text NOT NULL,
    "walletId" text NOT NULL,
    "ownerAddress" character varying(42) NOT NULL,
    "displayName" character varying(120),
    "groupId" text,
    "isDefault" boolean DEFAULT false NOT NULL,
    "isImported" boolean DEFAULT false NOT NULL,
    "avatarSeed" character varying(80),
    notes text,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL
);

CREATE TABLE public.web3_auth_sessions (
    id uuid NOT NULL,
    wallet_address character varying(42) NOT NULL,
    chain_id integer NOT NULL,
    session_version integer DEFAULT 1 NOT NULL,
    auth_epoch bigint NOT NULL,
    key_id character varying(64) NOT NULL,
    issued_at timestamp(6) with time zone NOT NULL,
    expires_at timestamp(6) with time zone NOT NULL,
    revoked_at timestamp(6) with time zone,
    revoke_reason character varying(16),
    revoked_by character varying(96),
    created_at timestamp(6) with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT web3_auth_session_revocation CHECK ((((revoked_at IS NULL) AND (revoke_reason IS NULL) AND (revoked_by IS NULL)) OR ((revoked_at IS NOT NULL) AND ((revoke_reason)::text = ANY ((ARRAY['logout'::character varying, 'operator'::character varying])::text[])) AND (revoked_by IS NOT NULL)))),
    CONSTRAINT web3_auth_session_shape CHECK (((chain_id > 0) AND (session_version > 0) AND (auth_epoch > 0) AND ((key_id)::text ~ '^[A-Za-z0-9._-]{1,64}$'::text) AND (expires_at > issued_at))),
    CONSTRAINT web3_auth_session_wallet CHECK ((((wallet_address)::text ~ '^0x[0-9a-f]{40}$'::text) AND ((wallet_address)::text <> '0x0000000000000000000000000000000000000000'::text)))
);

CREATE TABLE public.web3_events (
    id integer NOT NULL,
    chain_id integer NOT NULL,
    contract_address character varying(42) NOT NULL,
    event_name character varying(100) NOT NULL,
    tx_hash character varying(66) NOT NULL,
    log_index integer NOT NULL,
    block_number bigint NOT NULL,
    actor_address character varying(42),
    args jsonb DEFAULT '{}'::jsonb NOT NULL,
    occurred_at timestamp(3) without time zone,
    created_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    projection_version integer DEFAULT 0 NOT NULL,
    projection_completed_at timestamp(3) without time zone,
    projection_error text,
    projection_attempts integer DEFAULT 0 NOT NULL,
    projection_last_attempt_at timestamp(3) without time zone,
    CONSTRAINT web3_events_projection_attempts_check CHECK ((projection_attempts >= 0))
);

CREATE SEQUENCE public.web3_events_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.web3_events_id_seq OWNED BY public.web3_events.id;

CREATE TABLE public.web3_indexer_state (
    id integer NOT NULL,
    chain_id integer NOT NULL,
    contract_address character varying(42) NOT NULL,
    last_processed_block bigint NOT NULL,
    updated_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    confirmation_depth integer DEFAULT 6 NOT NULL,
    last_safe_block bigint DEFAULT 0 NOT NULL
);

CREATE SEQUENCE public.web3_indexer_state_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.web3_indexer_state_id_seq OWNED BY public.web3_indexer_state.id;

CREATE TABLE public.web3_transaction_actions (
    id text NOT NULL,
    chain_id integer NOT NULL,
    owner_address character varying(42) NOT NULL,
    client_action_id character varying(160) NOT NULL,
    action_type text,
    request_fingerprint character varying(64),
    status text DEFAULT 'pending'::text NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    created_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT web3_tx_actions_attempt_count_check CHECK ((attempt_count >= 0))
);

CREATE TABLE public.web3_transactions (
    id integer NOT NULL,
    chain_id integer NOT NULL,
    tx_hash character varying(66) NOT NULL,
    from_address character varying(42) NOT NULL,
    to_address character varying(42),
    contract_address character varying(42),
    value text,
    gas_used bigint,
    gas_price bigint,
    block_number bigint NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    tx_type text,
    metadata jsonb,
    created_at timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    action_id text,
    attempt_number integer DEFAULT 1 NOT NULL,
    sender_nonce bigint,
    block_hash character varying(66),
    confirmations integer DEFAULT 0 NOT NULL,
    submitted_at timestamp(3) without time zone,
    confirmed_at timestamp(3) without time zone,
    finalized_at timestamp(3) without time zone,
    last_checked_at timestamp(3) without time zone,
    check_attempts integer DEFAULT 0 NOT NULL,
    missing_streak integer DEFAULT 0 NOT NULL,
    replaced_by_hash character varying(66),
    failure_code text,
    failure_detail text,
    execution_context_state text,
    execution_context_mode text,
    execution_context_claim_digest text,
    execution_context_claimed_revision bigint,
    execution_context_operation text,
    execution_context_market text,
    execution_context_binding text,
    execution_context_consumer text,
    execution_context_manifest text,
    execution_context_reason text,
    execution_context_diagnostic text,
    execution_context_revision bigint,
    execution_context_classified_at timestamp(3) without time zone,
    CONSTRAINT web3_transactions_attempt_number_check CHECK ((attempt_number > 0)),
    CONSTRAINT web3_transactions_check_attempts_check CHECK ((check_attempts >= 0)),
    CONSTRAINT web3_transactions_confirmations_check CHECK ((confirmations >= 0)),
    CONSTRAINT web3_transactions_missing_streak_check CHECK ((missing_streak >= 0)),
    CONSTRAINT web3_transactions_sender_nonce_check CHECK (((sender_nonce IS NULL) OR (sender_nonce >= 0))),
    CONSTRAINT web3_tx_ctx_binding_check CHECK (((execution_context_binding IS NULL) OR (execution_context_binding ~ '^[0-9a-f]{64}$'::text))),
    CONSTRAINT web3_tx_ctx_claim_digest_check CHECK (((execution_context_claim_digest IS NULL) OR (execution_context_claim_digest ~ '^[0-9a-f]{64}$'::text))),
    CONSTRAINT web3_tx_ctx_claimed_revision_check CHECK (((execution_context_claimed_revision IS NULL) OR (execution_context_claimed_revision > 0))),
    CONSTRAINT web3_tx_ctx_consumer_check CHECK (((execution_context_consumer IS NULL) OR (execution_context_consumer ~ '^[a-z][a-z0-9-]{0,31}$'::text))),
    CONSTRAINT web3_tx_ctx_diagnostic_check CHECK (((execution_context_diagnostic IS NULL) OR ((length(execution_context_diagnostic) <= 160) AND (execution_context_diagnostic ~ '^(mode|registryRevision|operation|market|bindingDigest|reason|consumer|consumer[.]id|consumer[.]manifestDigest|unknownField|body):(missing|type|pattern|range|duplicate|size)(;(mode|registryRevision|operation|market|bindingDigest|reason|consumer|consumer[.]id|consumer[.]manifestDigest|unknownField|body):(missing|type|pattern|range|duplicate|size)){0,3}$'::text)))),
    CONSTRAINT web3_tx_ctx_manifest_check CHECK (((execution_context_manifest IS NULL) OR (execution_context_manifest ~ '^[0-9a-f]{64}$'::text))),
    CONSTRAINT web3_tx_ctx_market_check CHECK (((execution_context_market IS NULL) OR (execution_context_market ~ '^[A-Z0-9]{1,16}-[A-Z0-9]{1,16}$'::text))),
    CONSTRAINT web3_tx_ctx_mode_check CHECK (((execution_context_mode IS NULL) OR (execution_context_mode = ANY (ARRAY['bound'::text, 'unboundExit'::text])))),
    CONSTRAINT web3_tx_ctx_operation_check CHECK (((execution_context_operation IS NULL) OR (execution_context_operation = ANY (ARRAY['trade.approveCollateral'::text, 'trade.openPosition'::text, 'trade.closePosition'::text])))),
    CONSTRAINT web3_tx_ctx_reason_check CHECK (((execution_context_reason IS NULL) OR (execution_context_reason = ANY (ARRAY['malformed'::text, 'unknownRevision'::text, 'unknownBinding'::text, 'chainMismatch'::text, 'targetMismatch'::text, 'bytesMismatch'::text, 'evidenceIncomplete'::text, 'notAnExit'::text, 'authorityUnavailable'::text, 'noActiveRevision'::text, 'targetNotActive'::text, 'consumerIncompatible'::text])))),
    CONSTRAINT web3_tx_ctx_shape_check CHECK (((((execution_context_state IS NULL) AND (execution_context_mode IS NULL) AND (execution_context_claim_digest IS NULL) AND (execution_context_claimed_revision IS NULL) AND (execution_context_operation IS NULL) AND (execution_context_market IS NULL) AND (execution_context_binding IS NULL) AND (execution_context_consumer IS NULL) AND (execution_context_manifest IS NULL) AND (execution_context_reason IS NULL) AND (execution_context_diagnostic IS NULL) AND (execution_context_revision IS NULL) AND (execution_context_classified_at IS NULL)) OR ((execution_context_state = 'claimMismatch'::text) AND (execution_context_reason = 'malformed'::text) AND (execution_context_mode IS NULL) AND (execution_context_claim_digest IS NULL) AND (execution_context_claimed_revision IS NULL) AND (execution_context_operation IS NULL) AND (execution_context_market IS NULL) AND (execution_context_binding IS NULL) AND (execution_context_consumer IS NULL) AND (execution_context_manifest IS NULL) AND (execution_context_revision IS NULL) AND (execution_context_diagnostic IS NOT NULL) AND (execution_context_classified_at IS NOT NULL)) OR ((execution_context_state IS NOT NULL) AND (execution_context_reason IS DISTINCT FROM 'malformed'::text) AND (execution_context_claim_digest IS NOT NULL) AND (execution_context_operation IS NOT NULL) AND (execution_context_market IS NOT NULL) AND (execution_context_consumer IS NOT NULL) AND (execution_context_manifest IS NOT NULL) AND (execution_context_diagnostic IS NULL) AND (execution_context_classified_at IS NOT NULL) AND (((execution_context_mode = 'bound'::text) AND (execution_context_claimed_revision IS NOT NULL) AND (execution_context_binding IS NOT NULL) AND (((execution_context_state = 'claimUnverified'::text) AND (execution_context_reason IS NULL) AND (execution_context_revision IS NULL)) OR ((execution_context_state = 'recorded'::text) AND (execution_context_reason IS NULL) AND (execution_context_revision = execution_context_claimed_revision)) OR ((execution_context_state = 'claimMismatch'::text) AND (execution_context_reason = ANY (ARRAY['unknownRevision'::text, 'unknownBinding'::text, 'chainMismatch'::text, 'targetMismatch'::text, 'bytesMismatch'::text, 'evidenceIncomplete'::text])) AND (execution_context_revision IS NULL)))) OR ((execution_context_mode = 'unboundExit'::text) AND (execution_context_operation = 'trade.closePosition'::text) AND (execution_context_claimed_revision IS NULL) AND (execution_context_binding IS NULL) AND (execution_context_revision IS NULL) AND (((execution_context_state = 'unboundExit'::text) AND (execution_context_reason = ANY (ARRAY['authorityUnavailable'::text, 'noActiveRevision'::text, 'targetNotActive'::text, 'consumerIncompatible'::text]))) OR ((execution_context_state = 'claimMismatch'::text) AND (execution_context_reason = ANY (ARRAY['notAnExit'::text, 'evidenceIncomplete'::text])))))))) IS TRUE)),
    CONSTRAINT web3_tx_ctx_state_check CHECK (((execution_context_state IS NULL) OR (execution_context_state = ANY (ARRAY['claimUnverified'::text, 'recorded'::text, 'claimMismatch'::text, 'unboundExit'::text]))))
);

CREATE SEQUENCE public.web3_transactions_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.web3_transactions_id_seq OWNED BY public.web3_transactions.id;

CREATE TABLE public.web3_wallets (
    id text NOT NULL,
    address character varying(42) NOT NULL,
    "chainId" integer DEFAULT 11155111 NOT NULL,
    "userId" text,
    nonce character varying(100),
    "lastLoginAt" timestamp(3) without time zone,
    "createdAt" timestamp(3) without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    "updatedAt" timestamp(3) without time zone NOT NULL
);

ALTER TABLE ONLY public.execution_registry_snapshots ALTER COLUMN revision SET DEFAULT nextval('public.execution_registry_revision_seq'::regclass);

ALTER TABLE ONLY public.web3_events ALTER COLUMN id SET DEFAULT nextval('public.web3_events_id_seq'::regclass);

ALTER TABLE ONLY public.web3_indexer_state ALTER COLUMN id SET DEFAULT nextval('public.web3_indexer_state_id_seq'::regclass);

ALTER TABLE ONLY public.web3_transactions ALTER COLUMN id SET DEFAULT nextval('public.web3_transactions_id_seq'::regclass);

ALTER TABLE ONLY public."_ArticleToTag"
    ADD CONSTRAINT "_ArticleToTag_AB_pkey" PRIMARY KEY ("A", "B");

ALTER TABLE ONLY public.articles
    ADD CONSTRAINT articles_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.auth_sessions
    ADD CONSTRAINT auth_sessions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.bridge_relayer_heartbeat
    ADD CONSTRAINT bridge_relayer_heartbeat_pkey PRIMARY KEY (relayer_address);

ALTER TABLE ONLY public.bridge_relayer_log_failures
    ADD CONSTRAINT bridge_relayer_log_failures_pkey PRIMARY KEY (chain_id, block_number, log_index, tx_hash);

ALTER TABLE ONLY public.bridge_relayer_scan_progress
    ADD CONSTRAINT bridge_relayer_scan_progress_pkey PRIMARY KEY (chain_id);

ALTER TABLE ONLY public.bridge_transfers
    ADD CONSTRAINT bridge_transfers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.campaign_audit_events
    ADD CONSTRAINT campaign_audit_events_command_id_key UNIQUE (command_id);

ALTER TABLE ONLY public.campaign_audit_events
    ADD CONSTRAINT campaign_audit_events_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.campaign_commands
    ADD CONSTRAINT campaign_command_idempotency_key UNIQUE (actor, operation, target_id, client_command_id);

ALTER TABLE ONLY public.campaign_commands
    ADD CONSTRAINT campaign_commands_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.campaign_definitions
    ADD CONSTRAINT campaign_definitions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.campaign_definitions
    ADD CONSTRAINT campaign_definitions_slug_key UNIQUE (slug);

ALTER TABLE ONLY public.campaign_interaction_history
    ADD CONSTRAINT campaign_history_interaction_version_key UNIQUE (campaign_id, interaction_version);

ALTER TABLE ONLY public.campaign_interaction_history
    ADD CONSTRAINT campaign_interaction_history_command_id_key UNIQUE (command_id);

ALTER TABLE ONLY public.campaign_interaction_history
    ADD CONSTRAINT campaign_interaction_history_pkey PRIMARY KEY (campaign_id, wallet_address, kind, row_version);

ALTER TABLE ONLY public.campaign_interaction_states
    ADD CONSTRAINT campaign_interaction_states_pkey PRIMARY KEY (campaign_id, wallet_address, kind);

ALTER TABLE ONLY public.campaign_legacy_activations
    ADD CONSTRAINT campaign_legacy_activations_campaign_id_key UNIQUE (campaign_id);

ALTER TABLE ONLY public.campaign_legacy_activations
    ADD CONSTRAINT campaign_legacy_activations_command_id_key UNIQUE (command_id);

ALTER TABLE ONLY public.campaign_legacy_activations
    ADD CONSTRAINT campaign_legacy_activations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.campaign_legacy_activations
    ADD CONSTRAINT campaign_legacy_activations_publish_command_id_key UNIQUE (publish_command_id);

ALTER TABLE ONLY public.campaign_legacy_activations
    ADD CONSTRAINT campaign_legacy_activations_review_id_key UNIQUE (review_id);

ALTER TABLE ONLY public.campaign_legacy_archive_rows
    ADD CONSTRAINT campaign_legacy_archive_rows_pkey PRIMARY KEY (run_id, source_table, row_id);

ALTER TABLE ONLY public.campaign_legacy_fence_clock
    ADD CONSTRAINT campaign_legacy_fence_clock_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.campaign_legacy_import_items
    ADD CONSTRAINT campaign_legacy_import_items_pkey PRIMARY KEY (run_id, campaign_id);

ALTER TABLE ONLY public.campaign_legacy_import_runs
    ADD CONSTRAINT campaign_legacy_import_runs_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.campaign_legacy_review_decisions
    ADD CONSTRAINT campaign_legacy_review_decisions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.campaign_legacy_review_decisions
    ADD CONSTRAINT campaign_legacy_review_idempotency UNIQUE (decided_by, client_command_id);

ALTER TABLE ONLY public.campaign_legacy_review_decisions
    ADD CONSTRAINT campaign_legacy_review_once UNIQUE (run_id, campaign_id);

ALTER TABLE ONLY public.campaign_legacy_write_fences
    ADD CONSTRAINT campaign_legacy_write_fences_pkey PRIMARY KEY (campaign_id, run_id);

ALTER TABLE ONLY public.campaign_participants
    ADD CONSTRAINT campaign_participants_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.campaign_private_scopes
    ADD CONSTRAINT campaign_private_scopes_pkey PRIMARY KEY (wallet_address);

ALTER TABLE ONLY public.campaign_private_scopes
    ADD CONSTRAINT campaign_private_scopes_scope_id_key UNIQUE (scope_id);

ALTER TABLE ONLY public.campaign_publication_history
    ADD CONSTRAINT campaign_publication_history_command_id_key UNIQUE (command_id);

ALTER TABLE ONLY public.campaign_publication_history
    ADD CONSTRAINT campaign_publication_history_pkey PRIMARY KEY (campaign_id, publication_version);

ALTER TABLE ONLY public.campaign_publications
    ADD CONSTRAINT campaign_publications_pkey PRIMARY KEY (campaign_id);

ALTER TABLE ONLY public.campaign_publication_history
    ADD CONSTRAINT campaign_published_revision_key UNIQUE (campaign_id, publication_version, after_revision_id);

ALTER TABLE ONLY public.campaign_reminders
    ADD CONSTRAINT campaign_reminders_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.campaign_revisions
    ADD CONSTRAINT campaign_revision_identity_key UNIQUE (campaign_id, id);

ALTER TABLE ONLY public.campaign_revisions
    ADD CONSTRAINT campaign_revision_terms_key UNIQUE (campaign_id, id, material_terms_hash);

ALTER TABLE ONLY public.campaign_revisions
    ADD CONSTRAINT campaign_revisions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.categories
    ADD CONSTRAINT categories_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.comments
    ADD CONSTRAINT comments_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.connected_sites
    ADD CONSTRAINT connected_sites_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.consumer_receipts
    ADD CONSTRAINT consumer_receipts_pkey PRIMARY KEY (consumer_id, event_id);

ALTER TABLE ONLY public.conversations
    ADD CONSTRAINT conversations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.durable_outbox
    ADD CONSTRAINT durable_outbox_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.execution_registry_activations
    ADD CONSTRAINT execution_registry_activations_pkey PRIMARY KEY (activation_version);

ALTER TABLE ONLY public.execution_registry_bindings
    ADD CONSTRAINT execution_registry_bindings_digest_key UNIQUE (revision, binding_digest);

ALTER TABLE ONLY public.execution_registry_bindings
    ADD CONSTRAINT execution_registry_bindings_pkey PRIMARY KEY (revision, chain_id, operation, market);

ALTER TABLE ONLY public.execution_registry_entries
    ADD CONSTRAINT execution_registry_entries_digest_key UNIQUE (revision, entry_digest);

ALTER TABLE ONLY public.execution_registry_entries
    ADD CONSTRAINT execution_registry_entries_pkey PRIMARY KEY (revision, chain_id, name);

ALTER TABLE ONLY public.execution_registry_markets
    ADD CONSTRAINT execution_registry_markets_digest_key UNIQUE (revision, market_digest);

ALTER TABLE ONLY public.execution_registry_markets
    ADD CONSTRAINT execution_registry_markets_pkey PRIMARY KEY (revision, chain_id, symbol);

ALTER TABLE ONLY public.execution_registry_pointer
    ADD CONSTRAINT execution_registry_pointer_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.execution_registry_snapshots
    ADD CONSTRAINT execution_registry_snapshots_pkey PRIMARY KEY (revision);

ALTER TABLE ONLY public.hidden_assets
    ADD CONSTRAINT hidden_assets_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.i18n_audit_logs
    ADD CONSTRAINT i18n_audit_logs_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.i18n_catalog_revisions
    ADD CONSTRAINT i18n_catalog_revisions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.i18n_locales
    ADD CONSTRAINT i18n_locales_pkey PRIMARY KEY (code);

ALTER TABLE ONLY public.i18n_message_drafts
    ADD CONSTRAINT i18n_message_drafts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.interview_rooms
    ADD CONSTRAINT interview_rooms_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.launchpad_campaigns
    ADD CONSTRAINT launchpad_campaigns_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.lowcode_pages
    ADD CONSTRAINT lowcode_pages_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.market_candles
    ADD CONSTRAINT market_candles_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.messages
    ADD CONSTRAINT messages_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.mobile_docs
    ADD CONSTRAINT mobile_docs_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.model_statistics
    ADD CONSTRAINT model_statistics_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.notification_prefs
    ADD CONSTRAINT notification_prefs_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.oracle_price_observations
    ADD CONSTRAINT oracle_price_observations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.outbox_replay_intents
    ADD CONSTRAINT outbox_replay_intents_command_id_event_id_key UNIQUE (command_id, event_id);

ALTER TABLE ONLY public.outbox_replay_intents
    ADD CONSTRAINT outbox_replay_intents_pkey PRIMARY KEY (command_id);

ALTER TABLE ONLY public.outbox_replay_outcomes
    ADD CONSTRAINT outbox_replay_outcomes_command_id_event_id_key UNIQUE (command_id, event_id);

ALTER TABLE ONLY public.outbox_replay_outcomes
    ADD CONSTRAINT outbox_replay_outcomes_pkey PRIMARY KEY (command_id);

ALTER TABLE ONLY public.outbox_streams
    ADD CONSTRAINT outbox_stream_source_key UNIQUE (source, stream_key);

ALTER TABLE ONLY public.durable_outbox
    ADD CONSTRAINT outbox_stream_version_key UNIQUE (aggregate_id, aggregate_version);

ALTER TABLE ONLY public.outbox_streams
    ADD CONSTRAINT outbox_streams_pkey PRIMARY KEY (stream_key);

ALTER TABLE ONLY public.perp_orders
    ADD CONSTRAINT perp_orders_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.perp_positions
    ADD CONSTRAINT perp_positions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.perp_trades
    ADD CONSTRAINT perp_trades_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.profile_follows
    ADD CONSTRAINT profile_follows_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.room_participants
    ADD CONSTRAINT room_participants_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.room_sessions
    ADD CONSTRAINT room_sessions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.staking_actions
    ADD CONSTRAINT staking_actions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.staking_pools
    ADD CONSTRAINT staking_pools_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.tags
    ADD CONSTRAINT tags_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.token_candles
    ADD CONSTRAINT token_candles_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.token_comments
    ADD CONSTRAINT token_comments_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.token_holders
    ADD CONSTRAINT token_holders_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.token_trades
    ADD CONSTRAINT token_trades_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.tokens
    ADD CONSTRAINT tokens_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.user_preferences
    ADD CONSTRAINT user_preferences_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.user_stakes
    ADD CONSTRAINT user_stakes_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.user_statistics
    ADD CONSTRAINT user_statistics_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.verification_codes
    ADD CONSTRAINT verification_codes_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.wallet_groups
    ADD CONSTRAINT wallet_groups_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.wallet_profiles
    ADD CONSTRAINT wallet_profiles_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.web3_auth_sessions
    ADD CONSTRAINT web3_auth_sessions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.web3_events
    ADD CONSTRAINT web3_events_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.web3_indexer_state
    ADD CONSTRAINT web3_indexer_state_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.web3_transaction_actions
    ADD CONSTRAINT web3_transaction_actions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.web3_transactions
    ADD CONSTRAINT web3_transactions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.web3_wallets
    ADD CONSTRAINT web3_wallets_pkey PRIMARY KEY (id);

CREATE INDEX "_ArticleToTag_B_index" ON public."_ArticleToTag" USING btree ("B");

CREATE INDEX "articles_authorId_status_idx" ON public.articles USING btree ("authorId", status);

CREATE INDEX "articles_createdAt_idx" ON public.articles USING btree ("createdAt");

CREATE INDEX "articles_publishedAt_idx" ON public.articles USING btree ("publishedAt");

CREATE UNIQUE INDEX articles_slug_key ON public.articles USING btree (slug);

CREATE INDEX "articles_sourcePath_idx" ON public.articles USING btree ("sourcePath");

CREATE INDEX "articles_sourceType_sourceCollection_idx" ON public.articles USING btree ("sourceType", "sourceCollection");

CREATE INDEX articles_status_idx ON public.articles USING btree (status);

CREATE INDEX "articles_visibility_publishedAt_idx" ON public.articles USING btree (visibility, "publishedAt");

CREATE INDEX "auth_sessions_expiresAt_idx" ON public.auth_sessions USING btree ("expiresAt");

CREATE UNIQUE INDEX "auth_sessions_refreshTokenHash_key" ON public.auth_sessions USING btree ("refreshTokenHash");

CREATE INDEX "auth_sessions_userId_expiresAt_idx" ON public.auth_sessions USING btree ("userId", "expiresAt");

CREATE INDEX bridge_relayer_heartbeat_last_seen_at_idx ON public.bridge_relayer_heartbeat USING btree (last_seen_at DESC);

CREATE INDEX bridge_relayer_log_failures_unresolved_idx ON public.bridge_relayer_log_failures USING btree (resolved_at, last_failed_at DESC);

CREATE INDEX bridge_transfers_recipient_created_at_idx ON public.bridge_transfers USING btree (recipient, created_at DESC);

CREATE INDEX bridge_transfers_sender_created_at_idx ON public.bridge_transfers USING btree (sender, created_at DESC);

CREATE INDEX bridge_transfers_src_chain_id_deposit_block_idx ON public.bridge_transfers USING btree (src_chain_id, deposit_block);

CREATE UNIQUE INDEX bridge_transfers_src_chain_id_transfer_id_key ON public.bridge_transfers USING btree (src_chain_id, transfer_id);

CREATE INDEX bridge_transfers_status_created_at_idx ON public.bridge_transfers USING btree (status, created_at);

CREATE INDEX campaign_interaction_history_wallet_idx ON public.campaign_interaction_history USING btree (wallet_address, occurred_at, campaign_id, kind, row_version);

CREATE INDEX campaign_interaction_wallet_idx ON public.campaign_interaction_states USING btree (wallet_address, campaign_id);

CREATE INDEX campaign_legacy_archive_campaign_idx ON public.campaign_legacy_archive_rows USING btree (run_id, campaign_id);

CREATE UNIQUE INDEX campaign_participants_campaign_id_wallet_address_key ON public.campaign_participants USING btree (campaign_id, wallet_address);

CREATE INDEX campaign_participants_wallet_address_idx ON public.campaign_participants USING btree (wallet_address);

CREATE UNIQUE INDEX campaign_reminders_campaign_id_wallet_address_key ON public.campaign_reminders USING btree (campaign_id, wallet_address);

CREATE INDEX campaign_reminders_wallet_address_idx ON public.campaign_reminders USING btree (wallet_address);

CREATE UNIQUE INDEX categories_name_key ON public.categories USING btree (name);

CREATE UNIQUE INDEX categories_slug_key ON public.categories USING btree (slug);

CREATE INDEX "comments_articleId_createdAt_idx" ON public.comments USING btree ("articleId", "createdAt");

CREATE INDEX "comments_articleId_idx" ON public.comments USING btree ("articleId");

CREATE INDEX "comments_authorId_idx" ON public.comments USING btree ("authorId");

CREATE INDEX "comments_parentId_idx" ON public.comments USING btree ("parentId");

CREATE INDEX connected_sites_domain_idx ON public.connected_sites USING btree (domain);

CREATE INDEX "connected_sites_ownerAddress_lastConnectedAt_idx" ON public.connected_sites USING btree ("ownerAddress", "lastConnectedAt");

CREATE UNIQUE INDEX "connected_sites_ownerAddress_origin_key" ON public.connected_sites USING btree ("ownerAddress", origin);

CREATE INDEX "connected_sites_ownerAddress_riskLevel_idx" ON public.connected_sites USING btree ("ownerAddress", "riskLevel");

CREATE INDEX "conversations_createdAt_idx" ON public.conversations USING btree ("createdAt");

CREATE INDEX "conversations_lastMessageAt_idx" ON public.conversations USING btree ("lastMessageAt");

CREATE INDEX "conversations_modelProvider_idx" ON public.conversations USING btree ("modelProvider");

CREATE INDEX "conversations_model_createdAt_idx" ON public.conversations USING btree (model, "createdAt");

CREATE INDEX conversations_model_idx ON public.conversations USING btree (model);

CREATE INDEX conversations_status_idx ON public.conversations USING btree (status);

CREATE INDEX "conversations_userId_createdAt_idx" ON public.conversations USING btree ("userId", "createdAt");

CREATE INDEX "conversations_userId_idx" ON public.conversations USING btree ("userId");

CREATE INDEX durable_outbox_campaign_dead_letters_idx ON public.durable_outbox USING btree (dead_lettered_at, id) WHERE (((source)::text = 'campaign'::text) AND (published_at IS NULL) AND (dead_lettered_at IS NOT NULL));

CREATE INDEX durable_outbox_pending_idx ON public.durable_outbox USING btree (published_at, available_at);

CREATE UNIQUE INDEX "hidden_assets_ownerAddress_assetKey_key" ON public.hidden_assets USING btree ("ownerAddress", "assetKey");

CREATE INDEX "hidden_assets_ownerAddress_chainId_idx" ON public.hidden_assets USING btree ("ownerAddress", "chainId");

CREATE INDEX "hidden_assets_walletAddress_idx" ON public.hidden_assets USING btree ("walletAddress");

CREATE INDEX i18n_audit_logs_action_created_at_idx ON public.i18n_audit_logs USING btree (action, created_at DESC);

CREATE INDEX i18n_audit_logs_actor_created_at_idx ON public.i18n_audit_logs USING btree (actor, created_at DESC);

CREATE INDEX i18n_audit_logs_locale_code_created_at_idx ON public.i18n_audit_logs USING btree (locale_code, created_at DESC);

CREATE INDEX i18n_catalog_revisions_locale_code_published_at_idx ON public.i18n_catalog_revisions USING btree (locale_code, published_at DESC);

CREATE UNIQUE INDEX i18n_catalog_revisions_locale_code_version_key ON public.i18n_catalog_revisions USING btree (locale_code, version);

CREATE INDEX i18n_message_drafts_locale_code_namespace_message_key_idx ON public.i18n_message_drafts USING btree (locale_code, namespace, message_key);

CREATE UNIQUE INDEX i18n_message_drafts_locale_code_namespace_message_key_key ON public.i18n_message_drafts USING btree (locale_code, namespace, message_key);

CREATE INDEX "interview_rooms_createdAt_idx" ON public.interview_rooms USING btree ("createdAt");

CREATE INDEX "interview_rooms_creatorId_idx" ON public.interview_rooms USING btree ("creatorId");

CREATE INDEX "interview_rooms_roomName_idx" ON public.interview_rooms USING btree ("roomName");

CREATE UNIQUE INDEX "interview_rooms_roomName_key" ON public.interview_rooms USING btree ("roomName");

CREATE INDEX "interview_rooms_status_createdAt_idx" ON public.interview_rooms USING btree (status, "createdAt");

CREATE INDEX interview_rooms_status_idx ON public.interview_rooms USING btree (status);

CREATE INDEX launchpad_campaigns_end_date_idx ON public.launchpad_campaigns USING btree (end_date);

CREATE INDEX launchpad_campaigns_start_date_idx ON public.launchpad_campaigns USING btree (start_date);

CREATE INDEX launchpad_campaigns_status_idx ON public.launchpad_campaigns USING btree (status);

CREATE UNIQUE INDEX lowcode_pages_slug_key ON public.lowcode_pages USING btree (slug);

CREATE UNIQUE INDEX market_candles_source_symbol_resolution_bucket_start_key ON public.market_candles USING btree (source, symbol, resolution, bucket_start);

CREATE INDEX market_candles_symbol_resolution_bucket_start_idx ON public.market_candles USING btree (symbol, resolution, bucket_start DESC);

CREATE INDEX "messages_conversationId_createdAt_idx" ON public.messages USING btree ("conversationId", "createdAt");

CREATE INDEX "messages_conversationId_idx" ON public.messages USING btree ("conversationId");

CREATE INDEX "messages_createdAt_idx" ON public.messages USING btree ("createdAt");

CREATE INDEX messages_model_idx ON public.messages USING btree (model);

CREATE INDEX "messages_readAt_idx" ON public.messages USING btree ("readAt");

CREATE INDEX "messages_roleEnum_idx" ON public.messages USING btree ("roleEnum");

CREATE INDEX "messages_userId_idx" ON public.messages USING btree ("userId");

CREATE INDEX mobile_docs_category_idx ON public.mobile_docs USING btree (category);

CREATE UNIQUE INDEX "mobile_docs_filePath_key" ON public.mobile_docs USING btree ("filePath");

CREATE INDEX "mobile_docs_isHot_idx" ON public.mobile_docs USING btree ("isHot");

CREATE INDEX mobile_docs_published_idx ON public.mobile_docs USING btree (published);

CREATE INDEX "mobile_docs_sortOrder_idx" ON public.mobile_docs USING btree ("sortOrder");

CREATE INDEX model_statistics_date_idx ON public.model_statistics USING btree (date);

CREATE INDEX "model_statistics_modelProvider_idx" ON public.model_statistics USING btree ("modelProvider");

CREATE INDEX model_statistics_model_idx ON public.model_statistics USING btree (model);

CREATE UNIQUE INDEX "model_statistics_model_modelProvider_date_key" ON public.model_statistics USING btree (model, "modelProvider", date);

CREATE UNIQUE INDEX "notification_prefs_ownerAddress_key" ON public.notification_prefs USING btree ("ownerAddress");

CREATE INDEX oracle_price_observations_chain_id_feed_address_observed_at_idx ON public.oracle_price_observations USING btree (chain_id, feed_address, observed_at DESC);

CREATE UNIQUE INDEX oracle_price_observations_chain_id_feed_address_round_id_key ON public.oracle_price_observations USING btree (chain_id, feed_address, round_id);

CREATE INDEX oracle_price_observations_symbol_observed_at_idx ON public.oracle_price_observations USING btree (symbol, observed_at DESC);

CREATE UNIQUE INDEX outbox_replay_succeeded_generation_key ON public.outbox_replay_outcomes USING btree (event_id, replay_generation) WHERE ((status)::text = 'succeeded'::text);

CREATE INDEX perp_orders_account_idx ON public.perp_orders USING btree (account);

CREATE INDEX perp_orders_chain_id_account_idx ON public.perp_orders USING btree (chain_id, account);

CREATE INDEX perp_orders_chain_id_idx ON public.perp_orders USING btree (chain_id);

CREATE INDEX perp_orders_chain_id_status_idx ON public.perp_orders USING btree (chain_id, status);

CREATE INDEX perp_orders_chain_id_token_idx ON public.perp_orders USING btree (chain_id, token);

CREATE INDEX perp_orders_status_idx ON public.perp_orders USING btree (status);

CREATE INDEX perp_positions_account_idx ON public.perp_positions USING btree (account);

CREATE INDEX perp_positions_chain_id_account_idx ON public.perp_positions USING btree (chain_id, account);

CREATE INDEX perp_positions_chain_id_idx ON public.perp_positions USING btree (chain_id);

CREATE INDEX perp_positions_chain_id_status_idx ON public.perp_positions USING btree (chain_id, status);

CREATE INDEX perp_positions_chain_id_token_idx ON public.perp_positions USING btree (chain_id, token);

CREATE INDEX perp_positions_status_idx ON public.perp_positions USING btree (status);

CREATE INDEX perp_positions_token_idx ON public.perp_positions USING btree (token);

CREATE INDEX perp_trades_account_idx ON public.perp_trades USING btree (account);

CREATE INDEX perp_trades_chain_id_account_idx ON public.perp_trades USING btree (chain_id, account);

CREATE INDEX perp_trades_chain_id_idx ON public.perp_trades USING btree (chain_id);

CREATE INDEX perp_trades_chain_id_token_idx ON public.perp_trades USING btree (chain_id, token);

CREATE UNIQUE INDEX "perp_trades_chain_id_txHash_log_index_key" ON public.perp_trades USING btree (chain_id, "txHash", log_index);

CREATE INDEX perp_trades_chain_id_type_idx ON public.perp_trades USING btree (chain_id, type);

CREATE INDEX perp_trades_token_idx ON public.perp_trades USING btree (token);

CREATE INDEX perp_trades_type_idx ON public.perp_trades USING btree (type);

CREATE UNIQUE INDEX profile_follows_follower_address_following_address_key ON public.profile_follows USING btree (follower_address, following_address);

CREATE INDEX profile_follows_following_address_idx ON public.profile_follows USING btree (following_address);

CREATE INDEX room_participants_identity_idx ON public.room_participants USING btree (identity);

CREATE INDEX "room_participants_isActive_idx" ON public.room_participants USING btree ("isActive");

CREATE INDEX "room_participants_roomId_idx" ON public.room_participants USING btree ("roomId");

CREATE INDEX "room_participants_roomId_isActive_idx" ON public.room_participants USING btree ("roomId", "isActive");

CREATE INDEX "room_participants_userId_idx" ON public.room_participants USING btree ("userId");

CREATE INDEX "room_sessions_roomId_idx" ON public.room_sessions USING btree ("roomId");

CREATE INDEX "room_sessions_startedAt_idx" ON public.room_sessions USING btree ("startedAt");

CREATE INDEX staking_actions_chain_id_pool_address_idx ON public.staking_actions USING btree (chain_id, pool_address);

CREATE UNIQUE INDEX staking_actions_chain_id_tx_hash_log_index_key ON public.staking_actions USING btree (chain_id, tx_hash, log_index);

CREATE INDEX staking_actions_user_address_idx ON public.staking_actions USING btree (user_address);

CREATE INDEX staking_pools_chain_id_idx ON public.staking_pools USING btree (chain_id);

CREATE INDEX staking_pools_pool_type_idx ON public.staking_pools USING btree (pool_type);

CREATE INDEX staking_pools_status_idx ON public.staking_pools USING btree (status);

CREATE UNIQUE INDEX tags_name_key ON public.tags USING btree (name);

CREATE UNIQUE INDEX tags_slug_key ON public.tags USING btree (slug);

CREATE INDEX "token_candles_tokenId_resolution_time_idx" ON public.token_candles USING btree ("tokenId", resolution, "time" DESC);

CREATE UNIQUE INDEX "token_candles_tokenId_resolution_time_key" ON public.token_candles USING btree ("tokenId", resolution, "time");

CREATE INDEX "token_comments_createdAt_idx" ON public.token_comments USING btree ("createdAt");

CREATE INDEX "token_comments_tokenId_idx" ON public.token_comments USING btree ("tokenId");

CREATE INDEX "token_comments_userId_idx" ON public.token_comments USING btree ("userId");

CREATE INDEX "token_holders_tokenId_balance_idx" ON public.token_holders USING btree ("tokenId", balance DESC);

CREATE UNIQUE INDEX "token_holders_tokenId_user_address_key" ON public.token_holders USING btree ("tokenId", user_address);

CREATE INDEX token_trades_chain_id_block_number_log_index_idx ON public.token_trades USING btree (chain_id, block_number, log_index);

CREATE UNIQUE INDEX token_trades_chain_id_transaction_hash_log_index_key ON public.token_trades USING btree (chain_id, transaction_hash, log_index);

CREATE INDEX token_trades_timestamp_idx ON public.token_trades USING btree ("timestamp" DESC);

CREATE INDEX "token_trades_tokenId_idx" ON public.token_trades USING btree ("tokenId");

CREATE INDEX token_trades_user_address_idx ON public.token_trades USING btree (user_address);

CREATE UNIQUE INDEX tokens_address_key ON public.tokens USING btree (address);

CREATE INDEX tokens_chain_id_idx ON public.tokens USING btree (chain_id);

CREATE INDEX tokens_created_at_idx ON public.tokens USING btree (created_at DESC);

CREATE INDEX tokens_market_cap_idx ON public.tokens USING btree (market_cap DESC);

CREATE UNIQUE INDEX "user_preferences_ownerAddress_key" ON public.user_preferences USING btree ("ownerAddress");

CREATE INDEX user_stakes_pool_id_idx ON public.user_stakes USING btree (pool_id);

CREATE INDEX user_stakes_user_address_idx ON public.user_stakes USING btree (user_address);

CREATE INDEX user_statistics_date_idx ON public.user_statistics USING btree (date);

CREATE INDEX "user_statistics_userId_date_idx" ON public.user_statistics USING btree ("userId", date);

CREATE UNIQUE INDEX "user_statistics_userId_date_key" ON public.user_statistics USING btree ("userId", date);

CREATE UNIQUE INDEX users_email_key ON public.users USING btree (email);

CREATE UNIQUE INDEX users_username_key ON public.users USING btree (username);

CREATE INDEX verification_codes_code_type_idx ON public.verification_codes USING btree (code, type);

CREATE INDEX verification_codes_email_type_status_idx ON public.verification_codes USING btree (email, type, status);

CREATE INDEX "verification_codes_expiresAt_idx" ON public.verification_codes USING btree ("expiresAt");

CREATE UNIQUE INDEX "wallet_groups_ownerAddress_name_key" ON public.wallet_groups USING btree ("ownerAddress", name);

CREATE INDEX "wallet_groups_ownerAddress_sortOrder_idx" ON public.wallet_groups USING btree ("ownerAddress", "sortOrder");

CREATE INDEX "wallet_profiles_groupId_idx" ON public.wallet_profiles USING btree ("groupId");

CREATE INDEX "wallet_profiles_ownerAddress_idx" ON public.wallet_profiles USING btree ("ownerAddress");

CREATE INDEX "wallet_profiles_ownerAddress_isDefault_idx" ON public.wallet_profiles USING btree ("ownerAddress", "isDefault");

CREATE UNIQUE INDEX "wallet_profiles_ownerAddress_walletId_key" ON public.wallet_profiles USING btree ("ownerAddress", "walletId");

CREATE INDEX "wallet_profiles_walletId_idx" ON public.wallet_profiles USING btree ("walletId");

CREATE INDEX web3_auth_sessions_wallet_idx ON public.web3_auth_sessions USING btree (wallet_address, expires_at);

CREATE INDEX web3_events_actor_address_idx ON public.web3_events USING btree (actor_address);

CREATE INDEX web3_events_block_number_idx ON public.web3_events USING btree (block_number DESC);

CREATE INDEX web3_events_chain_id_contract_address_idx ON public.web3_events USING btree (chain_id, contract_address);

CREATE UNIQUE INDEX web3_events_chain_id_contract_address_tx_hash_log_index_key ON public.web3_events USING btree (chain_id, contract_address, tx_hash, log_index);

CREATE INDEX web3_events_contract_address_idx ON public.web3_events USING btree (contract_address);

CREATE INDEX web3_events_event_name_idx ON public.web3_events USING btree (event_name);

CREATE INDEX web3_events_projection_pending_idx ON public.web3_events USING btree (projection_completed_at, projection_version, id);

CREATE UNIQUE INDEX web3_indexer_state_chain_id_contract_address_key ON public.web3_indexer_state USING btree (chain_id, contract_address);

CREATE UNIQUE INDEX web3_transactions_action_attempt_key ON public.web3_transactions USING btree (action_id, attempt_number);

CREATE INDEX web3_transactions_block_number_idx ON public.web3_transactions USING btree (block_number DESC);

CREATE UNIQUE INDEX web3_transactions_chain_id_tx_hash_key ON public.web3_transactions USING btree (chain_id, tx_hash);

CREATE INDEX web3_transactions_contract_address_idx ON public.web3_transactions USING btree (contract_address);

CREATE INDEX web3_transactions_from_address_idx ON public.web3_transactions USING btree (from_address);

CREATE INDEX web3_transactions_nonce_idx ON public.web3_transactions USING btree (chain_id, from_address, sender_nonce);

CREATE INDEX web3_transactions_reconcile_idx ON public.web3_transactions USING btree (status, last_checked_at);

CREATE INDEX web3_transactions_status_idx ON public.web3_transactions USING btree (status);

CREATE INDEX web3_transactions_to_address_idx ON public.web3_transactions USING btree (to_address);

CREATE UNIQUE INDEX web3_tx_actions_client_key ON public.web3_transaction_actions USING btree (chain_id, owner_address, client_action_id);

CREATE INDEX web3_tx_actions_owner_idx ON public.web3_transaction_actions USING btree (owner_address, created_at DESC);

CREATE INDEX web3_wallets_address_idx ON public.web3_wallets USING btree (address);

CREATE UNIQUE INDEX web3_wallets_address_key ON public.web3_wallets USING btree (address);

CREATE INDEX "web3_wallets_userId_idx" ON public.web3_wallets USING btree ("userId");

CREATE TRIGGER campaign_audit_command BEFORE INSERT ON public.campaign_audit_events FOR EACH ROW EXECUTE FUNCTION public.campaign_command_attribution_guard();

CREATE TRIGGER campaign_interaction_command BEFORE INSERT ON public.campaign_interaction_history FOR EACH ROW EXECUTE FUNCTION public.campaign_command_attribution_guard();

CREATE CONSTRAINT TRIGGER campaign_interaction_evidence AFTER INSERT OR UPDATE ON public.campaign_interaction_states DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.campaign_evidence_commit_guard();

CREATE CONSTRAINT TRIGGER campaign_interaction_history_current AFTER INSERT ON public.campaign_interaction_history DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.campaign_history_commit_guard();

CREATE TRIGGER campaign_legacy_activation_truncate BEFORE TRUNCATE ON public.campaign_legacy_activations FOR EACH STATEMENT EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER campaign_legacy_activation_write BEFORE INSERT OR DELETE OR UPDATE ON public.campaign_legacy_activations FOR EACH ROW EXECUTE FUNCTION public.campaign_legacy_activation_guard();

CREATE TRIGGER campaign_legacy_archive_truncate BEFORE TRUNCATE ON public.campaign_legacy_archive_rows FOR EACH STATEMENT EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER campaign_legacy_archive_write BEFORE INSERT OR DELETE OR UPDATE ON public.campaign_legacy_archive_rows FOR EACH ROW EXECUTE FUNCTION public.campaign_legacy_capture_guard();

CREATE CONSTRAINT TRIGGER campaign_legacy_definition_activation AFTER INSERT ON public.campaign_definitions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN ((new.legacy_snapshot ? 'activationId'::text)) EXECUTE FUNCTION public.campaign_legacy_definition_guard();

CREATE TRIGGER campaign_legacy_fence BEFORE INSERT OR DELETE OR UPDATE ON public.campaign_participants FOR EACH ROW EXECUTE FUNCTION public.campaign_legacy_fence_guard();

CREATE TRIGGER campaign_legacy_fence BEFORE INSERT OR DELETE OR UPDATE ON public.campaign_reminders FOR EACH ROW EXECUTE FUNCTION public.campaign_legacy_fence_guard();

CREATE TRIGGER campaign_legacy_fence BEFORE INSERT OR DELETE OR UPDATE ON public.launchpad_campaigns FOR EACH ROW EXECUTE FUNCTION public.campaign_legacy_fence_guard();

CREATE TRIGGER campaign_legacy_fence_clock_truncate BEFORE TRUNCATE ON public.campaign_legacy_fence_clock FOR EACH STATEMENT EXECUTE FUNCTION public.campaign_append_only();

-- The singleton starts at zero before its append-only guard is installed.
INSERT INTO public.campaign_legacy_fence_clock (id, ticks) VALUES (1, 0);

CREATE TRIGGER campaign_legacy_fence_clock_write BEFORE INSERT OR DELETE OR UPDATE ON public.campaign_legacy_fence_clock FOR EACH ROW EXECUTE FUNCTION public.campaign_legacy_fence_clock_guard();

CREATE TRIGGER campaign_legacy_fence_record BEFORE INSERT OR DELETE OR UPDATE ON public.campaign_legacy_write_fences FOR EACH ROW EXECUTE FUNCTION public.campaign_legacy_fence_record_guard();

CREATE TRIGGER campaign_legacy_fence_record_truncate BEFORE TRUNCATE ON public.campaign_legacy_write_fences FOR EACH STATEMENT EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER campaign_legacy_fence_truncate BEFORE TRUNCATE ON public.campaign_participants FOR EACH STATEMENT EXECUTE FUNCTION public.campaign_legacy_fence_truncate_guard();

CREATE TRIGGER campaign_legacy_fence_truncate BEFORE TRUNCATE ON public.campaign_reminders FOR EACH STATEMENT EXECUTE FUNCTION public.campaign_legacy_fence_truncate_guard();

CREATE TRIGGER campaign_legacy_fence_truncate BEFORE TRUNCATE ON public.launchpad_campaigns FOR EACH STATEMENT EXECUTE FUNCTION public.campaign_legacy_fence_truncate_guard();

CREATE CONSTRAINT TRIGGER campaign_legacy_import_command AFTER INSERT ON public.campaign_commands DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (((new.operation)::text = 'legacy-import'::text)) EXECUTE FUNCTION public.campaign_legacy_import_command_guard();

CREATE TRIGGER campaign_legacy_item_truncate BEFORE TRUNCATE ON public.campaign_legacy_import_items FOR EACH STATEMENT EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER campaign_legacy_item_write BEFORE INSERT OR DELETE OR UPDATE ON public.campaign_legacy_import_items FOR EACH ROW EXECUTE FUNCTION public.campaign_legacy_capture_guard();

CREATE TRIGGER campaign_legacy_review_truncate BEFORE TRUNCATE ON public.campaign_legacy_review_decisions FOR EACH STATEMENT EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER campaign_legacy_review_write BEFORE INSERT OR DELETE OR UPDATE ON public.campaign_legacy_review_decisions FOR EACH ROW EXECUTE FUNCTION public.campaign_legacy_review_guard();

CREATE TRIGGER campaign_legacy_run_transition BEFORE INSERT OR DELETE OR UPDATE ON public.campaign_legacy_import_runs FOR EACH ROW EXECUTE FUNCTION public.campaign_legacy_run_guard();

CREATE TRIGGER campaign_legacy_run_truncate BEFORE TRUNCATE ON public.campaign_legacy_import_runs FOR EACH STATEMENT EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER campaign_publication_command BEFORE INSERT ON public.campaign_publication_history FOR EACH ROW EXECUTE FUNCTION public.campaign_command_attribution_guard();

CREATE CONSTRAINT TRIGGER campaign_publication_evidence AFTER INSERT OR UPDATE ON public.campaign_publications DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.campaign_evidence_commit_guard();

CREATE CONSTRAINT TRIGGER campaign_publication_history_current AFTER INSERT ON public.campaign_publication_history DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.campaign_history_commit_guard();

CREATE TRIGGER campaign_publication_truncate BEFORE TRUNCATE ON public.campaign_publications FOR EACH STATEMENT EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER campaign_publication_update BEFORE INSERT OR DELETE OR UPDATE ON public.campaign_publications FOR EACH ROW EXECUTE FUNCTION public.campaign_publication_guard();

CREATE TRIGGER campaign_state_truncate BEFORE TRUNCATE ON public.campaign_interaction_states FOR EACH STATEMENT EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER campaign_state_update BEFORE INSERT OR DELETE OR UPDATE ON public.campaign_interaction_states FOR EACH ROW EXECUTE FUNCTION public.campaign_state_guard();

CREATE TRIGGER execution_registry_activations_truncate BEFORE TRUNCATE ON public.execution_registry_activations FOR EACH STATEMENT EXECUTE FUNCTION public.execution_registry_immutable();

CREATE TRIGGER execution_registry_activations_write BEFORE INSERT OR DELETE OR UPDATE ON public.execution_registry_activations FOR EACH ROW EXECUTE FUNCTION public.execution_registry_activation_guard();

CREATE TRIGGER execution_registry_bindings_truncate BEFORE TRUNCATE ON public.execution_registry_bindings FOR EACH STATEMENT EXECUTE FUNCTION public.execution_registry_immutable();

CREATE TRIGGER execution_registry_bindings_write BEFORE INSERT OR DELETE OR UPDATE ON public.execution_registry_bindings FOR EACH ROW EXECUTE FUNCTION public.execution_registry_member_guard();

CREATE TRIGGER execution_registry_entries_truncate BEFORE TRUNCATE ON public.execution_registry_entries FOR EACH STATEMENT EXECUTE FUNCTION public.execution_registry_immutable();

CREATE TRIGGER execution_registry_entries_write BEFORE INSERT OR DELETE OR UPDATE ON public.execution_registry_entries FOR EACH ROW EXECUTE FUNCTION public.execution_registry_member_guard();

CREATE TRIGGER execution_registry_markets_truncate BEFORE TRUNCATE ON public.execution_registry_markets FOR EACH STATEMENT EXECUTE FUNCTION public.execution_registry_immutable();

CREATE TRIGGER execution_registry_markets_write BEFORE INSERT OR DELETE OR UPDATE ON public.execution_registry_markets FOR EACH ROW EXECUTE FUNCTION public.execution_registry_member_guard();

CREATE TRIGGER execution_registry_pointer_truncate BEFORE TRUNCATE ON public.execution_registry_pointer FOR EACH STATEMENT EXECUTE FUNCTION public.execution_registry_immutable();

CREATE TRIGGER execution_registry_pointer_write BEFORE INSERT OR DELETE OR UPDATE ON public.execution_registry_pointer FOR EACH ROW EXECUTE FUNCTION public.execution_registry_pointer_guard();

CREATE TRIGGER execution_registry_snapshots_insert BEFORE INSERT ON public.execution_registry_snapshots FOR EACH ROW EXECUTE FUNCTION public.execution_registry_header_guard();

CREATE CONSTRAINT TRIGGER execution_registry_snapshots_sealed AFTER INSERT ON public.execution_registry_snapshots DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.execution_registry_sealed_at_commit();

CREATE TRIGGER execution_registry_snapshots_truncate BEFORE TRUNCATE ON public.execution_registry_snapshots FOR EACH STATEMENT EXECUTE FUNCTION public.execution_registry_immutable();

CREATE TRIGGER execution_registry_snapshots_write BEFORE DELETE OR UPDATE ON public.execution_registry_snapshots FOR EACH ROW EXECUTE FUNCTION public.execution_registry_seal_guard();

CREATE TRIGGER immutable_rows BEFORE DELETE OR UPDATE ON public.campaign_audit_events FOR EACH ROW EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER immutable_rows BEFORE DELETE OR UPDATE ON public.campaign_commands FOR EACH ROW EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER immutable_rows BEFORE DELETE OR UPDATE ON public.campaign_definitions FOR EACH ROW EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER immutable_rows BEFORE DELETE OR UPDATE ON public.campaign_interaction_history FOR EACH ROW EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER immutable_rows BEFORE DELETE OR UPDATE ON public.campaign_private_scopes FOR EACH ROW EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER immutable_rows BEFORE DELETE OR UPDATE ON public.campaign_publication_history FOR EACH ROW EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER immutable_rows BEFORE DELETE OR UPDATE ON public.campaign_revisions FOR EACH ROW EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER immutable_rows BEFORE DELETE OR UPDATE ON public.consumer_receipts FOR EACH ROW EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER immutable_rows BEFORE DELETE OR UPDATE ON public.outbox_replay_intents FOR EACH ROW EXECUTE FUNCTION public.outbox_replay_immutable();

CREATE TRIGGER immutable_rows BEFORE DELETE OR UPDATE ON public.outbox_replay_outcomes FOR EACH ROW EXECUTE FUNCTION public.outbox_replay_immutable();

CREATE TRIGGER immutable_truncate BEFORE TRUNCATE ON public.campaign_audit_events FOR EACH STATEMENT EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER immutable_truncate BEFORE TRUNCATE ON public.campaign_commands FOR EACH STATEMENT EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER immutable_truncate BEFORE TRUNCATE ON public.campaign_definitions FOR EACH STATEMENT EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER immutable_truncate BEFORE TRUNCATE ON public.campaign_interaction_history FOR EACH STATEMENT EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER immutable_truncate BEFORE TRUNCATE ON public.campaign_private_scopes FOR EACH STATEMENT EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER immutable_truncate BEFORE TRUNCATE ON public.campaign_publication_history FOR EACH STATEMENT EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER immutable_truncate BEFORE TRUNCATE ON public.campaign_revisions FOR EACH STATEMENT EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER immutable_truncate BEFORE TRUNCATE ON public.consumer_receipts FOR EACH STATEMENT EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER immutable_truncate BEFORE TRUNCATE ON public.outbox_replay_intents FOR EACH STATEMENT EXECUTE FUNCTION public.outbox_replay_immutable();

CREATE TRIGGER immutable_truncate BEFORE TRUNCATE ON public.outbox_replay_outcomes FOR EACH STATEMENT EXECUTE FUNCTION public.outbox_replay_immutable();

CREATE CONSTRAINT TRIGGER outbox_event_stream_version AFTER INSERT ON public.durable_outbox DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.outbox_event_commit_guard();

CREATE TRIGGER outbox_payload_truncate BEFORE TRUNCATE ON public.durable_outbox FOR EACH STATEMENT EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER outbox_payload_update BEFORE DELETE OR UPDATE ON public.durable_outbox FOR EACH ROW EXECUTE FUNCTION public.outbox_payload_guard();

CREATE TRIGGER outbox_replay_intent_stamp BEFORE INSERT ON public.outbox_replay_intents FOR EACH ROW EXECUTE FUNCTION public.outbox_replay_intent_stamp();

CREATE CONSTRAINT TRIGGER outbox_replay_outcome_evidence AFTER INSERT ON public.outbox_replay_outcomes DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.outbox_replay_outcome_commit_guard();

CREATE TRIGGER outbox_replay_outcome_stamp BEFORE INSERT ON public.outbox_replay_outcomes FOR EACH ROW EXECUTE FUNCTION public.outbox_replay_outcome_stamp();

CREATE CONSTRAINT TRIGGER outbox_replay_reset_evidence AFTER UPDATE ON public.durable_outbox DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.outbox_replay_reset_commit_guard();

CREATE TRIGGER outbox_replay_transition BEFORE INSERT OR UPDATE ON public.durable_outbox FOR EACH ROW EXECUTE FUNCTION public.outbox_replay_transition_guard();

CREATE CONSTRAINT TRIGGER outbox_stream_evidence AFTER INSERT OR UPDATE ON public.outbox_streams DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.outbox_stream_commit_guard();

CREATE TRIGGER outbox_stream_truncate BEFORE TRUNCATE ON public.outbox_streams FOR EACH STATEMENT EXECUTE FUNCTION public.campaign_append_only();

CREATE TRIGGER outbox_stream_update BEFORE INSERT OR DELETE OR UPDATE ON public.outbox_streams FOR EACH ROW EXECUTE FUNCTION public.outbox_stream_guard();

CREATE TRIGGER web3_auth_session_immutable BEFORE UPDATE ON public.web3_auth_sessions FOR EACH ROW EXECUTE FUNCTION public.web3_auth_session_guard();

ALTER TABLE ONLY public."_ArticleToTag"
    ADD CONSTRAINT "_ArticleToTag_A_fkey" FOREIGN KEY ("A") REFERENCES public.articles(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE ONLY public."_ArticleToTag"
    ADD CONSTRAINT "_ArticleToTag_B_fkey" FOREIGN KEY ("B") REFERENCES public.tags(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE ONLY public.articles
    ADD CONSTRAINT "articles_authorId_fkey" FOREIGN KEY ("authorId") REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE ONLY public.articles
    ADD CONSTRAINT "articles_categoryId_fkey" FOREIGN KEY ("categoryId") REFERENCES public.categories(id) ON UPDATE CASCADE ON DELETE SET NULL;

ALTER TABLE ONLY public.auth_sessions
    ADD CONSTRAINT "auth_sessions_userId_fkey" FOREIGN KEY ("userId") REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE ONLY public.campaign_audit_events
    ADD CONSTRAINT campaign_audit_events_campaign_id_fkey FOREIGN KEY (campaign_id) REFERENCES public.campaign_definitions(id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_audit_events
    ADD CONSTRAINT campaign_audit_events_command_id_fkey FOREIGN KEY (command_id) REFERENCES public.campaign_commands(id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_definitions
    ADD CONSTRAINT campaign_definitions_id_fkey FOREIGN KEY (id) REFERENCES public.launchpad_campaigns(id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_interaction_history
    ADD CONSTRAINT campaign_history_accepted_publication_fk FOREIGN KEY (campaign_id, accepted_publication_version, accepted_revision_id) REFERENCES public.campaign_publication_history(campaign_id, publication_version, after_revision_id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_interaction_history
    ADD CONSTRAINT campaign_history_accepted_terms_fk FOREIGN KEY (campaign_id, accepted_revision_id, accepted_terms_hash) REFERENCES public.campaign_revisions(campaign_id, id, material_terms_hash) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_publication_history
    ADD CONSTRAINT campaign_history_after_revision_fk FOREIGN KEY (campaign_id, after_revision_id) REFERENCES public.campaign_revisions(campaign_id, id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_publication_history
    ADD CONSTRAINT campaign_history_before_revision_fk FOREIGN KEY (campaign_id, before_revision_id) REFERENCES public.campaign_revisions(campaign_id, id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_interaction_history
    ADD CONSTRAINT campaign_history_state_fk FOREIGN KEY (campaign_id, wallet_address, kind) REFERENCES public.campaign_interaction_states(campaign_id, wallet_address, kind) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_interaction_history
    ADD CONSTRAINT campaign_interaction_history_command_id_fkey FOREIGN KEY (command_id) REFERENCES public.campaign_commands(id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_interaction_states
    ADD CONSTRAINT campaign_interaction_states_campaign_id_fkey FOREIGN KEY (campaign_id) REFERENCES public.campaign_definitions(id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_legacy_activations
    ADD CONSTRAINT campaign_legacy_activations_campaign_id_fkey FOREIGN KEY (campaign_id) REFERENCES public.campaign_definitions(id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_legacy_activations
    ADD CONSTRAINT campaign_legacy_activations_campaign_id_revision_id_fkey FOREIGN KEY (campaign_id, revision_id) REFERENCES public.campaign_revisions(campaign_id, id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_legacy_activations
    ADD CONSTRAINT campaign_legacy_activations_command_id_fkey FOREIGN KEY (command_id) REFERENCES public.campaign_commands(id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_legacy_activations
    ADD CONSTRAINT campaign_legacy_activations_publish_command_id_fkey FOREIGN KEY (publish_command_id) REFERENCES public.campaign_commands(id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_legacy_activations
    ADD CONSTRAINT campaign_legacy_activations_review_id_fkey FOREIGN KEY (review_id) REFERENCES public.campaign_legacy_review_decisions(id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_legacy_activations
    ADD CONSTRAINT campaign_legacy_activations_run_id_campaign_id_fkey FOREIGN KEY (run_id, campaign_id) REFERENCES public.campaign_legacy_import_items(run_id, campaign_id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_legacy_archive_rows
    ADD CONSTRAINT campaign_legacy_archive_rows_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.campaign_legacy_import_runs(id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_legacy_import_items
    ADD CONSTRAINT campaign_legacy_import_items_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.campaign_legacy_import_runs(id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_legacy_review_decisions
    ADD CONSTRAINT campaign_legacy_review_decisions_run_id_campaign_id_fkey FOREIGN KEY (run_id, campaign_id) REFERENCES public.campaign_legacy_import_items(run_id, campaign_id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_legacy_write_fences
    ADD CONSTRAINT campaign_legacy_write_fences_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.campaign_legacy_import_runs(id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_publication_history
    ADD CONSTRAINT campaign_publication_history_campaign_id_fkey FOREIGN KEY (campaign_id) REFERENCES public.campaign_definitions(id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_publication_history
    ADD CONSTRAINT campaign_publication_history_command_id_fkey FOREIGN KEY (command_id) REFERENCES public.campaign_commands(id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_publications
    ADD CONSTRAINT campaign_publication_revision_fk FOREIGN KEY (campaign_id, active_revision_id) REFERENCES public.campaign_revisions(campaign_id, id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_publications
    ADD CONSTRAINT campaign_publications_campaign_id_fkey FOREIGN KEY (campaign_id) REFERENCES public.campaign_definitions(id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_revisions
    ADD CONSTRAINT campaign_revisions_campaign_id_fkey FOREIGN KEY (campaign_id) REFERENCES public.campaign_definitions(id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_interaction_states
    ADD CONSTRAINT campaign_state_accepted_publication_fk FOREIGN KEY (campaign_id, accepted_publication_version, accepted_revision_id) REFERENCES public.campaign_publication_history(campaign_id, publication_version, after_revision_id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.campaign_interaction_states
    ADD CONSTRAINT campaign_state_accepted_terms_fk FOREIGN KEY (campaign_id, accepted_revision_id, accepted_terms_hash) REFERENCES public.campaign_revisions(campaign_id, id, material_terms_hash) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.comments
    ADD CONSTRAINT "comments_articleId_fkey" FOREIGN KEY ("articleId") REFERENCES public.articles(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE ONLY public.comments
    ADD CONSTRAINT "comments_authorId_fkey" FOREIGN KEY ("authorId") REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE ONLY public.comments
    ADD CONSTRAINT "comments_parentId_fkey" FOREIGN KEY ("parentId") REFERENCES public.comments(id) ON UPDATE CASCADE ON DELETE SET NULL;

ALTER TABLE ONLY public.consumer_receipts
    ADD CONSTRAINT consumer_receipts_event_id_fkey FOREIGN KEY (event_id) REFERENCES public.durable_outbox(id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.conversations
    ADD CONSTRAINT "conversations_userId_fkey" FOREIGN KEY ("userId") REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE ONLY public.execution_registry_activations
    ADD CONSTRAINT execution_registry_activations_revision_fkey FOREIGN KEY (revision) REFERENCES public.execution_registry_snapshots(revision);

ALTER TABLE ONLY public.execution_registry_bindings
    ADD CONSTRAINT execution_registry_bindings_market_fkey FOREIGN KEY (revision, chain_id, market) REFERENCES public.execution_registry_markets(revision, chain_id, symbol);

ALTER TABLE ONLY public.execution_registry_bindings
    ADD CONSTRAINT execution_registry_bindings_target_fkey FOREIGN KEY (revision, chain_id, target_name) REFERENCES public.execution_registry_entries(revision, chain_id, name);

ALTER TABLE ONLY public.execution_registry_entries
    ADD CONSTRAINT execution_registry_entries_revision_fkey FOREIGN KEY (revision) REFERENCES public.execution_registry_snapshots(revision);

ALTER TABLE ONLY public.execution_registry_markets
    ADD CONSTRAINT execution_registry_markets_collateral_fkey FOREIGN KEY (revision, chain_id, collateral_token) REFERENCES public.execution_registry_entries(revision, chain_id, name);

ALTER TABLE ONLY public.execution_registry_markets
    ADD CONSTRAINT execution_registry_markets_index_fkey FOREIGN KEY (revision, chain_id, index_token) REFERENCES public.execution_registry_entries(revision, chain_id, name);

ALTER TABLE ONLY public.execution_registry_markets
    ADD CONSTRAINT execution_registry_markets_pm_fkey FOREIGN KEY (revision, chain_id, position_manager) REFERENCES public.execution_registry_entries(revision, chain_id, name);

ALTER TABLE ONLY public.execution_registry_pointer
    ADD CONSTRAINT execution_registry_pointer_revision_fkey FOREIGN KEY (active_revision) REFERENCES public.execution_registry_snapshots(revision);

ALTER TABLE ONLY public.i18n_catalog_revisions
    ADD CONSTRAINT i18n_catalog_revisions_locale_code_fkey FOREIGN KEY (locale_code) REFERENCES public.i18n_locales(code) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE ONLY public.i18n_message_drafts
    ADD CONSTRAINT i18n_message_drafts_locale_code_fkey FOREIGN KEY (locale_code) REFERENCES public.i18n_locales(code) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE ONLY public.interview_rooms
    ADD CONSTRAINT "interview_rooms_creatorId_fkey" FOREIGN KEY ("creatorId") REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE SET NULL;

ALTER TABLE ONLY public.lowcode_pages
    ADD CONSTRAINT "lowcode_pages_ownerId_fkey" FOREIGN KEY ("ownerId") REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE ONLY public.messages
    ADD CONSTRAINT "messages_conversationId_fkey" FOREIGN KEY ("conversationId") REFERENCES public.conversations(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE ONLY public.messages
    ADD CONSTRAINT "messages_userId_fkey" FOREIGN KEY ("userId") REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE SET NULL;

ALTER TABLE ONLY public.outbox_replay_intents
    ADD CONSTRAINT outbox_replay_intents_event_id_fkey FOREIGN KEY (event_id) REFERENCES public.durable_outbox(id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.outbox_replay_outcomes
    ADD CONSTRAINT outbox_replay_outcomes_command_id_event_id_fkey FOREIGN KEY (command_id, event_id) REFERENCES public.outbox_replay_intents(command_id, event_id) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.durable_outbox
    ADD CONSTRAINT outbox_source_stream_fk FOREIGN KEY (source, aggregate_id) REFERENCES public.outbox_streams(source, stream_key) ON UPDATE RESTRICT ON DELETE RESTRICT;

ALTER TABLE ONLY public.room_participants
    ADD CONSTRAINT "room_participants_roomId_fkey" FOREIGN KEY ("roomId") REFERENCES public.interview_rooms(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE ONLY public.room_participants
    ADD CONSTRAINT "room_participants_userId_fkey" FOREIGN KEY ("userId") REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE SET NULL;

ALTER TABLE ONLY public.room_sessions
    ADD CONSTRAINT "room_sessions_roomId_fkey" FOREIGN KEY ("roomId") REFERENCES public.interview_rooms(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE ONLY public.token_candles
    ADD CONSTRAINT "token_candles_tokenId_fkey" FOREIGN KEY ("tokenId") REFERENCES public.tokens(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE ONLY public.token_comments
    ADD CONSTRAINT "token_comments_tokenId_fkey" FOREIGN KEY ("tokenId") REFERENCES public.tokens(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE ONLY public.token_comments
    ADD CONSTRAINT "token_comments_userId_fkey" FOREIGN KEY ("userId") REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE ONLY public.token_holders
    ADD CONSTRAINT "token_holders_tokenId_fkey" FOREIGN KEY ("tokenId") REFERENCES public.tokens(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE ONLY public.token_trades
    ADD CONSTRAINT "token_trades_tokenId_fkey" FOREIGN KEY ("tokenId") REFERENCES public.tokens(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE ONLY public.user_stakes
    ADD CONSTRAINT user_stakes_pool_id_fkey FOREIGN KEY (pool_id) REFERENCES public.staking_pools(id) ON UPDATE CASCADE ON DELETE RESTRICT;

ALTER TABLE ONLY public.user_statistics
    ADD CONSTRAINT "user_statistics_userId_fkey" FOREIGN KEY ("userId") REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE ONLY public.verification_codes
    ADD CONSTRAINT "verification_codes_userId_fkey" FOREIGN KEY ("userId") REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE ONLY public.wallet_profiles
    ADD CONSTRAINT "wallet_profiles_groupId_fkey" FOREIGN KEY ("groupId") REFERENCES public.wallet_groups(id) ON UPDATE CASCADE ON DELETE SET NULL;

ALTER TABLE ONLY public.wallet_profiles
    ADD CONSTRAINT "wallet_profiles_walletId_fkey" FOREIGN KEY ("walletId") REFERENCES public.web3_wallets(id) ON UPDATE CASCADE ON DELETE CASCADE;

ALTER TABLE ONLY public.web3_transactions
    ADD CONSTRAINT web3_transactions_action_id_fkey FOREIGN KEY (action_id) REFERENCES public.web3_transaction_actions(id) ON UPDATE CASCADE ON DELETE SET NULL;

ALTER TABLE ONLY public.web3_transactions
    ADD CONSTRAINT web3_tx_ctx_revision_fkey FOREIGN KEY (execution_context_revision) REFERENCES public.execution_registry_snapshots(revision);

ALTER TABLE ONLY public.web3_wallets
    ADD CONSTRAINT "web3_wallets_userId_fkey" FOREIGN KEY ("userId") REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE SET NULL;

REVOKE ALL ON FUNCTION public.execution_registry_activate(p_expected_version bigint, p_revision bigint, p_actor text, p_reason text, p_code_observations jsonb) FROM PUBLIC;

REVOKE ALL ON FUNCTION public.outbox_replay_immutable() FROM PUBLIC;

REVOKE ALL ON FUNCTION public.outbox_replay_intent_stamp() FROM PUBLIC;

REVOKE ALL ON FUNCTION public.outbox_replay_outcome_commit_guard() FROM PUBLIC;

REVOKE ALL ON FUNCTION public.outbox_replay_outcome_stamp() FROM PUBLIC;

REVOKE ALL ON FUNCTION public.outbox_replay_reset_commit_guard() FROM PUBLIC;

REVOKE ALL ON FUNCTION public.outbox_replay_snapshot(g bigint, a integer, d timestamp with time zone, e text, p timestamp with time zone, v timestamp with time zone, l timestamp with time zone, observed timestamp with time zone) FROM PUBLIC;

REVOKE ALL ON FUNCTION public.outbox_replay_transition_guard() FROM PUBLIC;
