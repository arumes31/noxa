-- 068_audit_log_partitions.sql — (301) monthly range partitions for audit_log.
--
-- audit_log becomes a table partitioned by created_at (UTC months) with a
-- DEFAULT partition, so an insert never fails for want of a partition. Every
-- existing row, id and the id sequence are kept; ids stay globally unique
-- because they still come from audit_log_id_seq. The primary key must include
-- the partition key, so it becomes (id, created_at); nothing references
-- audit_log rows by foreign key.
--
-- The serving process has DML privileges only and cannot create or drop
-- partitions. noxa-migrate creates partitions for the coming months on every
-- run (and moves rows that reached the DEFAULT partition into them); see
-- Store.MaintainAuditPartitions.
--
-- chat_messages stays unpartitioned: reactions, pins, polls and discussions
-- reference chat_messages(id) by foreign key, and a partitioned table can only
-- be referenced through a key that includes the partition column.

-- Copies a table's privileges (minus the owner's) onto another table, after
-- removing whatever the destination was granted by default privileges, so a
-- partition carries exactly the grants of its parent: table privileges with
-- their grant options, and column privileges for columns of the same name.
-- Invoker rights: only the owner running noxa-migrate can use it.
CREATE OR REPLACE FUNCTION public.noxa_copy_table_acl(src regclass, dst regclass)
RETURNS void LANGUAGE plpgsql AS $fn$
DECLARE
    r RECORD;
BEGIN
    FOR r IN
        SELECT DISTINCT CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE a.grantee::regrole::text END AS who
        FROM pg_catalog.pg_class c, pg_catalog.aclexplode(c.relacl) a
        WHERE c.oid = dst AND a.grantee <> c.relowner
    LOOP
        EXECUTE pg_catalog.format('REVOKE ALL ON %s FROM %s', dst, r.who);
    END LOOP;
    FOR r IN
        SELECT DISTINCT att.attname, CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE a.grantee::regrole::text END AS who
        FROM pg_catalog.pg_attribute att, pg_catalog.aclexplode(att.attacl) a, pg_catalog.pg_class c
        WHERE att.attrelid = dst AND c.oid = dst AND att.attnum > 0 AND NOT att.attisdropped
          AND att.attacl IS NOT NULL AND a.grantee <> c.relowner
    LOOP
        EXECUTE pg_catalog.format('REVOKE ALL (%I) ON %s FROM %s', r.attname, dst, r.who);
    END LOOP;
    -- regrole::text is already quoted where an identifier needs it.
    FOR r IN
        SELECT CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE a.grantee::regrole::text END AS who,
               a.is_grantable, pg_catalog.string_agg(DISTINCT a.privilege_type, ', ') AS privileges
        FROM pg_catalog.pg_class c, pg_catalog.aclexplode(c.relacl) a
        WHERE c.oid = src AND a.grantee <> c.relowner
        GROUP BY 1, 2
    LOOP
        EXECUTE pg_catalog.format('GRANT %s ON %s TO %s%s', r.privileges, dst, r.who,
            CASE WHEN r.is_grantable THEN ' WITH GRANT OPTION' ELSE '' END);
    END LOOP;
    FOR r IN
        SELECT att.attname, CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE a.grantee::regrole::text END AS who,
               a.is_grantable, pg_catalog.string_agg(DISTINCT a.privilege_type, ', ') AS privileges
        FROM pg_catalog.pg_attribute att, pg_catalog.aclexplode(att.attacl) a, pg_catalog.pg_class c
        WHERE att.attrelid = src AND c.oid = src AND att.attnum > 0 AND NOT att.attisdropped
          AND att.attacl IS NOT NULL AND a.grantee <> c.relowner
          AND EXISTS (SELECT 1 FROM pg_catalog.pg_attribute d WHERE d.attrelid = dst
                      AND d.attname = att.attname AND d.attnum > 0 AND NOT d.attisdropped)
        GROUP BY 1, 2, 3
    LOOP
        EXECUTE pg_catalog.format('GRANT %s (%I) ON %s TO %s%s', r.privileges, r.attname, dst, r.who,
            CASE WHEN r.is_grantable THEN ' WITH GRANT OPTION' ELSE '' END);
    END LOOP;
END
$fn$;
REVOKE ALL ON FUNCTION public.noxa_copy_table_acl(regclass, regclass) FROM PUBLIC;

DO $$
DECLARE
    first_month DATE;
    last_month  DATE;
    m           DATE;
    part        TEXT;
    before_rows BIGINT;
    after_rows  BIGINT;
BEGIN
    IF EXISTS (SELECT 1 FROM pg_catalog.pg_partitioned_table WHERE partrelid = 'public.audit_log'::regclass) THEN
        RETURN;
    END IF;
    LOCK TABLE public.audit_log IN ACCESS EXCLUSIVE MODE;
    SELECT count(*) INTO before_rows FROM public.audit_log;

    ALTER TABLE public.audit_log RENAME TO audit_log_unpartitioned;
    ALTER INDEX IF EXISTS public.audit_log_pkey RENAME TO audit_log_unpartitioned_pkey;
    ALTER INDEX IF EXISTS public.idx_audit_log_created RENAME TO idx_audit_log_unpartitioned_created;
    ALTER INDEX IF EXISTS public.idx_audit_log_retention RENAME TO idx_audit_log_unpartitioned_retention;

    CREATE TABLE public.audit_log (
        id                BIGINT      NOT NULL DEFAULT pg_catalog.nextval('public.audit_log_id_seq'::regclass),
        actor_unique_id   TEXT        NOT NULL DEFAULT '',
        action            TEXT        NOT NULL,
        target            TEXT        NOT NULL DEFAULT '',
        detail            TEXT        NOT NULL DEFAULT '',
        created_at        TIMESTAMPTZ NOT NULL DEFAULT pg_catalog.now(),
        scope_channel_ids BIGINT[],
        CONSTRAINT audit_log_pkey PRIMARY KEY (id, created_at)
    ) PARTITION BY RANGE (created_at);
    -- Keep the sequence (and its current value) when the old table is dropped.
    ALTER SEQUENCE public.audit_log_id_seq OWNED BY public.audit_log.id;
    CREATE INDEX idx_audit_log_created ON public.audit_log (id DESC);
    CREATE INDEX idx_audit_log_retention ON public.audit_log (created_at, id);
    PERFORM public.noxa_copy_table_acl('public.audit_log_unpartitioned'::regclass, 'public.audit_log'::regclass);

    CREATE TABLE public.audit_log_default PARTITION OF public.audit_log DEFAULT;
    PERFORM public.noxa_copy_table_acl('public.audit_log'::regclass, 'public.audit_log_default'::regclass);

    -- A partition for every month that holds rows (no empty months between an
    -- old outlier and today), plus the current month and twelve ahead. Rows
    -- after that window stay in the DEFAULT partition until noxa-migrate
    -- gives them a month of their own.
    first_month := pg_catalog.date_trunc('month', pg_catalog.now() AT TIME ZONE 'UTC')::date;
    last_month := (first_month + interval '12 months')::date;
    FOR m IN
        SELECT month FROM (
            SELECT DISTINCT pg_catalog.date_trunc('month', created_at AT TIME ZONE 'UTC')::date AS month
            FROM public.audit_log_unpartitioned
            UNION
            SELECT pg_catalog.generate_series(first_month, last_month, interval '1 month')::date
        ) months
        WHERE month <= last_month
        ORDER BY month
    LOOP
        part := 'audit_log_' || pg_catalog.to_char(m, 'YYYYMM');
        EXECUTE pg_catalog.format('CREATE TABLE public.%I PARTITION OF public.audit_log FOR VALUES FROM (%L) TO (%L)',
            part, (m::timestamp AT TIME ZONE 'UTC'), ((m + interval '1 month')::timestamp AT TIME ZONE 'UTC'));
        PERFORM public.noxa_copy_table_acl('public.audit_log'::regclass, ('public.' || pg_catalog.quote_ident(part))::regclass);
    END LOOP;

    INSERT INTO public.audit_log (id, actor_unique_id, action, target, detail, created_at, scope_channel_ids)
    SELECT id, actor_unique_id, action, target, detail, created_at, scope_channel_ids
    FROM public.audit_log_unpartitioned;
    SELECT count(*) INTO after_rows FROM public.audit_log;
    IF after_rows <> before_rows THEN
        RAISE EXCEPTION 'audit_log partitioning copied % of % rows', after_rows, before_rows;
    END IF;

    DROP TABLE public.audit_log_unpartitioned;
END
$$;
