-- @scope:       database
-- @resultsets:  root:object, masked_columns:array, row_level_security:array, audit_specifications:array, encryption_keys:array, external_data_sources:array
-- @permissions: CONNECT, VIEW ANY DEFINITION
-- @timeout:     60
-- @min_version: 13
--
-- Runs once per user database, with the connection context switched to it.
--
-- The protections a database carries in its own catalogue: which columns are
-- masked, which tables filter their own rows, whether anything is audited, and
-- what it is encrypted or federated with.
--
-- WHY THIS COLLECTOR EXISTS. 40.security/020.database-principals.sql answers
-- who exists in the database and what they were granted. That is a complete
-- answer to one question and it says nothing about a second one that clients
-- increasingly have to answer to an auditor: what happens when a principal who
-- IS allowed to read a table reads it. Dynamic data masking rewrites what they
-- see, row-level security removes rows before they see them, Always Encrypted
-- means the engine itself cannot read the column, and an audit specification
-- decides whether the read is written down. None of the four appears in a
-- permissions list, and until now none appeared in this archive at all.
--
-- 20.databases/026.persisted-sku-features.sql is the neighbouring collector and
-- it does not cover this. It reads sys.dm_db_persisted_sku_features, which
-- answers an edition question: which Enterprise-era features have physically
-- marked this database. Masking, row-level security and auditing are not
-- edition features, so that view is silent about all three by design.
--
-- THE FLOOR IS SQL SERVER 2016, AND THE ONE THING THAT WOULD HAVE DESERVED AN
-- OLDER FLOOR IS ELSEWHERE. sys.masked_columns, sys.security_policies,
-- sys.column_master_keys and sys.external_data_sources all arrived in 2016, so
-- the file cannot run below it. sys.database_audit_specifications goes back to
-- 2008 and gating it here would lose it on a 2012 or 2014 instance, which
-- would be a real loss: "is this database audited" is the most valuable line in
-- the file. That is why 40.security/030.server-surface.sql carries the
-- instance-level audit picture with no version gate. An instance below 2016
-- therefore still learns that an audit exists, that it is running, and how
-- many action groups feed it; what it loses is which database specifications
-- exist, which is detail rather than the answer.
--
-- WHAT IS READ OUT OF EACH, AND WHAT IS LEFT ON THE INSTANCE.
--
-- Masking. The masking function is projected verbatim, because partial() and
-- default() are entirely different guarantees and the argument list of
-- partial() is where the guarantee actually lives. It is a function
-- specification and not data.
--
-- Row-level security. The predicate definition is projected, and the decision
-- deserves stating: a predicate is an expression, and an expression can embed
-- a literal. Two things make it collectable anyway. It names a predicate
-- function that 70.schema/080.modules.sql already collects the body of, so
-- refusing it here would protect nothing. And a row-level security policy that
-- cannot be read cannot be reviewed, which is the only reason to collect it at
-- all: the finding is almost never "there is a policy" and almost always
-- "the FILTER predicate exists and the BLOCK predicate does not", so a
-- principal who can see nothing can still write rows they will never see
-- again.
--
-- Always Encrypted. Column master keys are named with their store provider and
-- key path, never with key material, which is not readable through any
-- catalogue. The key path is what says whether the key lives in a certificate
-- store on one person's workstation, which is the usual finding.
--
-- External data sources. location and type are projected; connection_options
-- is not. That field is free-form and provider-defined, and it is the one
-- place in this file where a string could carry something that does not belong
-- in an archive.
--
-- WHY THE COUNTS ARE IN THE ROOT. Every array here is empty on almost every
-- database, and an empty array is also what a failed read produces. A count
-- per array, taken separately, is what separates "this database has no masked
-- columns" from "this did not run".
--
-- Because an all-empty run proves nothing, four of the five arrays were
-- exercised on 16.0.4265.3 against a database built for the purpose and then
-- dropped: two masked columns carrying email() and partial(0,"XXXX",4), a
-- schema-bound security policy with a FILTER predicate and deliberately no
-- BLOCK one, a column master key on a CurrentUser/My certificate path, and a
-- database audit specification of two action groups pointing at an audit that
-- was never enabled so that no file was written. All four rendered and the
-- counts agreed with the rows, including policies_without_block, which is the
-- column this file exists for. The fifth, external data sources, was not
-- exercised: creating one needs PolyBase installed, which the lab instances do
-- not have, so that array is written from the catalogue's documented shape and
-- not from a measurement.
--
-- NO JUDGEMENT IS APPLIED. Masking is not a security control against a
-- principal who can also run a query of their own devising, and Microsoft says
-- so; row-level security with only a filter predicate is a legitimate design
-- for a read-only reporting database; and most databases are correctly not
-- audited. What each of them is worth depends on who holds which permission,
-- which is 020.database-principals.sql's answer and not this file's.
--
-- Not collected, deliberately:
--   sys.column_encryption_keys       (the encrypted key values, and the
--     enclave computations flag, are on the master key row that matters)
--   connection_options               (see above)
--   sys.security_policies.is_schema_bound is projected, but not the policy's
--     own definition: a policy is a container, its predicates carry the logic

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT
    DB_NAME()                                                   AS [database],
    CONVERT(varchar(23), SYSDATETIME(), 126)                    AS [collected_at],
    (SELECT COUNT(*) FROM sys.masked_columns WHERE is_masked = 1)
                                                                AS [counts.masked_columns],
    (SELECT COUNT(DISTINCT object_id) FROM sys.masked_columns WHERE is_masked = 1)
                                                                AS [counts.tables_with_masking],
    (SELECT COUNT(*) FROM sys.security_policies)                AS [counts.security_policies],
    (SELECT COUNT(*) FROM sys.security_policies WHERE is_enabled = 1)
                                                                AS [counts.security_policies_enabled],
    -- A policy with no BLOCK predicate filters reads and permits writes that
    -- the writer will never see again. Counted here because it is the finding.
    (SELECT COUNT(*) FROM sys.security_policies AS p
      WHERE NOT EXISTS (SELECT 1 FROM sys.security_predicates AS sp
                         WHERE sp.object_id = p.object_id
                           AND sp.predicate_type_desc = 'BLOCK'))
                                                                AS [counts.policies_without_block],
    (SELECT COUNT(*) FROM sys.database_audit_specifications)    AS [counts.audit_specifications],
    (SELECT COUNT(*) FROM sys.database_audit_specifications WHERE is_state_enabled = 1)
                                                                AS [counts.audit_specifications_enabled],
    (SELECT COUNT(*) FROM sys.column_master_keys)               AS [counts.column_master_keys],
    (SELECT COUNT(*) FROM sys.columns WHERE encryption_type IS NOT NULL)
                                                                AS [counts.encrypted_columns],
    (SELECT COUNT(*) FROM sys.external_data_sources)            AS [counts.external_data_sources]
OPTION (RECOMPILE, MAXDOP 1);

/* Masked columns, with the function that masks them. The type is projected
   beside it because masking a column and then exposing the same value through
   a computed column or an index is the usual way the protection is defeated,
   and 70.schema/060.columns.sql carries the rest of the picture. */
SELECT
    OBJECT_SCHEMA_NAME(m.object_id) + '.' + OBJECT_NAME(m.object_id)
                                                                AS [table],
    m.name                                                      AS [column],
    -- default(), email(), partial(prefix,"padding",suffix) or random(a,b).
    -- The argument list is the guarantee: partial(0,"XXXX",4) hides nothing of
    -- a four-character value.
    m.masking_function                                          AS [masking_function],
    TYPE_NAME(m.user_type_id)                                   AS [type],
    m.max_length                                                AS [max_length],
    CAST(m.is_nullable AS bit)                                  AS [is_nullable],
    CAST(m.is_computed AS bit)                                  AS [is_computed]
FROM sys.masked_columns AS m
WHERE m.is_masked = 1
ORDER BY OBJECT_SCHEMA_NAME(m.object_id), OBJECT_NAME(m.object_id), m.column_id
OPTION (RECOMPILE, MAXDOP 1);

/* One row per predicate, not per policy: a policy is a container and the
   predicates are what it does. operation is NULL for a FILTER predicate and
   names the statement for a BLOCK one, which is how the two are told apart at
   a glance. */
SELECT
    p.name                                                      AS [policy],
    CAST(p.is_enabled AS bit)                                   AS [is_enabled],
    CAST(p.is_schema_bound AS bit)                              AS [is_schema_bound],
    sp.predicate_type_desc                                      AS [predicate_type],
    sp.operation_desc                                           AS [operation],
    OBJECT_SCHEMA_NAME(sp.target_object_id) + '.'
        + OBJECT_NAME(sp.target_object_id)                      AS [target_table],
    -- The expression itself. It names a predicate function whose body
    -- 70.schema/080.modules.sql already collects, so withholding it here would
    -- protect nothing and would make the policy unreviewable.
    sp.predicate_definition                                     AS [predicate]
FROM sys.security_policies AS p
JOIN sys.security_predicates AS sp ON sp.object_id = p.object_id
ORDER BY p.name, sp.security_predicate_id
OPTION (RECOMPILE, MAXDOP 1);

/* Database audit specifications and what they capture. A specification with no
   details writes nothing, and an enabled specification pointing at a disabled
   audit writes nothing either, so both states are projected rather than
   summarised into one boolean. */
SELECT
    s.name                                                      AS [specification],
    CAST(s.is_state_enabled AS bit)                             AS [is_enabled],
    CONVERT(varchar(23), s.create_date, 126)                    AS [created_at],
    CONVERT(varchar(23), s.modify_date, 126)                    AS [modified_at],
    (SELECT COUNT(*) FROM sys.database_audit_specification_details AS d
      WHERE d.database_specification_id = s.database_specification_id)
                                                                AS [details],
    -- The action groups, which is what is actually being recorded. A
    -- specification on SCHEMA_OBJECT_ACCESS_GROUP and one on
    -- DATABASE_ROLE_MEMBER_CHANGE_GROUP are not the same control.
    STUFF((SELECT N', ' + d.audit_action_name
             FROM sys.database_audit_specification_details AS d
            WHERE d.database_specification_id = s.database_specification_id
            ORDER BY d.audit_action_name
            FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)'), 1, 2, N'')
                                                                AS [action_groups]
FROM sys.database_audit_specifications AS s
ORDER BY s.name
OPTION (RECOMPILE, MAXDOP 1);

/* Column master keys. Key material is not readable and is not collected; the
   path is, because it is what says where the key actually lives. */
SELECT
    k.name                                                      AS [master_key],
    k.key_store_provider_name                                   AS [store_provider],
    -- A path under CurrentUser/My is a certificate in one person's Windows
    -- profile: when that profile goes, so does every column it protects.
    k.key_path                                                  AS [key_path],
    CONVERT(varchar(23), k.create_date, 126)                    AS [created_at],
    (SELECT COUNT(*) FROM sys.column_encryption_key_values AS v
      WHERE v.column_master_key_id = k.column_master_key_id)    AS [encryption_keys],
    (SELECT COUNT(*) FROM sys.columns AS c
       JOIN sys.column_encryption_keys AS ck
         ON ck.column_encryption_key_id = c.column_encryption_key_id
       JOIN sys.column_encryption_key_values AS v
         ON v.column_encryption_key_id = ck.column_encryption_key_id
      WHERE v.column_master_key_id = k.column_master_key_id)    AS [protected_columns]
FROM sys.column_master_keys AS k
ORDER BY k.name
OPTION (RECOMPILE, MAXDOP 1);

/* External data sources: what this database reaches outside itself.
   connection_options is not read, for the reason in the header. */
SELECT
    e.name                                                      AS [name],
    e.type_desc                                                 AS [type],
    e.location                                                  AS [location],
    e.database_name                                             AS [remote_database],
    -- Which credential it presents, by name. 030.server-surface.sql carries
    -- the server-scoped credentials; a database-scoped one lives in
    -- sys.database_scoped_credentials and is named here rather than inventoried.
    (SELECT c.name FROM sys.database_scoped_credentials AS c
      WHERE c.credential_id = e.credential_id)                  AS [credential],
    CAST(e.pushdown AS varchar(10))                             AS [pushdown]
FROM sys.external_data_sources AS e
ORDER BY e.name
OPTION (RECOMPILE, MAXDOP 1);
