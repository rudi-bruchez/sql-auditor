-- @scope:       instance
-- @resultsets:  root:object, principals:array, role_members:array, server_permissions:array, sql_login_policy:array
-- @permissions: CONNECT, VIEW ANY DEFINITION
-- @timeout:     60
--
-- Who can connect to this instance, what they are members of, and what has
-- been granted to them directly.
--
-- Why this collector exists: every audit needs it and the corpus had nothing.
-- Until this file, a report could describe an instance in detail and say
-- nothing whatever about who holds sysadmin on it.
--
-- NO SECRET IS COLLECTED. sys.sql_logins carries password_hash and
-- password_hash is a credential — it is offline-crackable, and an audit
-- archive is copied, mailed and archived far more casually than a database.
-- It is not projected here, and it must not be added. What IS collected is
-- the policy around the password: whether expiration and complexity are
-- checked, and when it was last set. Those are the facts a finding rests on.
--
-- SIDs are not collected either. They identify a Windows account precisely
-- enough to be worth withholding, and the name already answers the audit
-- question.
--
-- Direct server-level grants are listed in full rather than counted, because
-- the interesting ones are rare and each is its own decision — a single
-- CONTROL SERVER granted to an application login is the whole finding, and a
-- count of "37 permissions" would bury it.
--
-- NO JUDGEMENT IS APPLIED. A sysadmin member is not a defect: an instance
-- needs administrators. Whether four of them, or a login named after a person
-- who left, or an application account among them is a problem depends on the
-- organisation — and that is the analysis layer's call, made against the
-- client's own rules.
--
-- Database-level principals and role memberships are NOT here: they are
-- per-database and need their own database-scoped collector, which does not
-- exist yet. A reader must not take this file as covering database users.
--
-- SQL Server 2012 is the floor. Not collected for that reason:
--   sys.server_principals.is_fixed_role   (2012 has it; kept)
--   AUTHENTICATION_TYPE / Azure AD columns (2016+, Azure)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

SELECT SYSDATETIME()                                              AS [collected_at],
       (SELECT COUNT(*) FROM sys.server_principals AS p
        WHERE p.type IN ('S','U','G') AND p.is_disabled = 0)      AS [counts.enabled_principals],
       (SELECT COUNT(*) FROM sys.server_principals AS p
        WHERE p.type IN ('S','U','G') AND p.is_disabled = 1)      AS [counts.disabled_principals],
       (SELECT COUNT(*) FROM sys.server_principals AS p WHERE p.type = 'S') AS [counts.sql_logins],
       (SELECT COUNT(*) FROM sys.server_principals AS p WHERE p.type = 'U') AS [counts.windows_logins],
       (SELECT COUNT(*) FROM sys.server_principals AS p WHERE p.type = 'G') AS [counts.windows_groups],
       (SELECT COUNT(*) FROM sys.server_role_members AS rm
        JOIN sys.server_principals AS r ON r.principal_id = rm.role_principal_id
        WHERE r.name = 'sysadmin')                                AS [counts.sysadmin_members],
       (SELECT COUNT(*) FROM sys.sql_logins AS l
        WHERE l.is_policy_checked = 0)                            AS [counts.sql_logins_without_policy],
       (SELECT COUNT(*) FROM sys.sql_logins AS l
        WHERE l.is_expiration_checked = 0)                        AS [counts.sql_logins_without_expiration],
       (SELECT CAST(is_disabled AS int) FROM sys.server_principals
        WHERE sid = 0x01)                                         AS [sa.is_disabled],
       (SELECT name FROM sys.server_principals WHERE sid = 0x01)  AS [sa.name],
       CONVERT(int, SERVERPROPERTY('IsIntegratedSecurityOnly'))   AS [windows_authentication_only]
OPTION (RECOMPILE, MAXDOP 1);

/* type is projected raw AND described: 'S' means nothing to a reader, and
   type_desc alone loses the letter every other view keys on. */
SELECT p.name                                                     AS [name],
       p.type                                                     AS [type],
       p.type_desc                                                AS [type_desc],
       CAST(p.is_disabled AS int)                                 AS [is_disabled],
       p.create_date                                              AS [create_date],
       p.modify_date                                              AS [modify_date],
       p.default_database_name                                    AS [default_database],
       p.default_language_name                                    AS [default_language]
FROM sys.server_principals AS p
WHERE p.type IN ('S', 'U', 'G')
ORDER BY p.name
OPTION (RECOMPILE, MAXDOP 1);

SELECT r.name                                                     AS [role],
       m.name                                                     AS [member],
       m.type_desc                                                AS [member_type],
       CAST(m.is_disabled AS int)                                 AS [member_is_disabled]
FROM sys.server_role_members AS rm
JOIN sys.server_principals AS r ON r.principal_id = rm.role_principal_id
JOIN sys.server_principals AS m ON m.principal_id = rm.member_principal_id
ORDER BY r.name, m.name
OPTION (RECOMPILE, MAXDOP 1);

/* Grants made directly to a principal, as opposed to inherited from a role.
   CONNECT SQL on the endpoint is the one every login has and it is kept: its
   ABSENCE for a login that exists is itself a finding. */
SELECT pr.name                                                    AS [grantee],
       pe.class_desc                                              AS [class],
       pe.permission_name                                         AS [permission],
       pe.state_desc                                              AS [state],
       CASE WHEN pe.class = 101 THEN OBJECT_NAME(pe.major_id) END AS [on_object]
FROM sys.server_permissions AS pe
JOIN sys.server_principals AS pr ON pr.principal_id = pe.grantee_principal_id
WHERE pr.type IN ('S', 'U', 'G')
ORDER BY pr.name, pe.permission_name
OPTION (RECOMPILE, MAXDOP 1);

/* password_hash is deliberately absent — see the header. */
SELECT l.name                                                     AS [name],
       CAST(l.is_policy_checked AS int)                           AS [is_policy_checked],
       CAST(l.is_expiration_checked AS int)                       AS [is_expiration_checked],
       l.modify_date                                              AS [modify_date],
       -- LOGINPROPERTY returns sql_variant whatever is asked of it, and the
       -- encoder cannot render sql_variant: projected raw, every one of these
       -- four reached the archive empty, once per login. The base types are
       -- documented — PasswordLastSetTime is datetime, IsLocked, IsMustChange
       -- and BadPasswordCount are int — so the conversion asserts nothing the
       -- function does not already promise.
       CONVERT(datetime, LOGINPROPERTY(l.name, 'PasswordLastSetTime'))
                                                                  AS [password_last_set],
       CONVERT(bit,      LOGINPROPERTY(l.name, 'IsLocked'))       AS [is_locked],
       CONVERT(bit,      LOGINPROPERTY(l.name, 'IsMustChange'))   AS [must_change],
       CONVERT(int,      LOGINPROPERTY(l.name, 'BadPasswordCount'))
                                                                  AS [bad_password_count]
FROM sys.sql_logins AS l
ORDER BY l.name
OPTION (RECOMPILE, MAXDOP 1);
