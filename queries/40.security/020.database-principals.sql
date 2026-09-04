-- @scope:       database
-- @resultsets:  root:object, principals:array, role_members:array, database_permissions:array, object_permissions:array
-- @permissions: CONNECT, VIEW ANY DEFINITION
-- @timeout:     60
--
-- Who exists inside this database, what they are members of, and what has been
-- granted to them.
--
-- Runs once per user database, with the connection context switched to it.
--
-- 010.principals.sql covers the server and says in its own header that
-- database-level principals need their own database-scoped collector. This is
-- that collector, and until it existed the security section of a report could
-- only speak about server-level sysadmin membership — which is the shallow half
-- of the question. On an estate where nine principals hold sysadmin on each
-- instance, the other half is where the finding usually is.
--
-- THE SAME TWO REFUSALS AS 010, FOR THE SAME REASONS. No SID is projected: it
-- identifies a Windows account precisely enough to be worth withholding, and
-- the name already answers the audit question. And no judgement is applied — a
-- member of db_owner is not a defect, an application often needs one. Whether
-- THIS db_owner is a problem depends on the organisation's own rules, which is
-- the analysis layer's business.
--
-- ORPHANS ARE DERIVED HERE RATHER THAN LEFT TO BE INFERRED, because inferring
-- them needs the SID this file deliberately does not project. A user whose SID
-- matches no server principal cannot log in, and it is usually the residue of a
-- restore from another instance.
--
-- CONTAINED USERS ARE NOT ORPHANS AND THE TEST SAYS SO. A user created with a
-- password inside the database has no login by design, and
-- authentication_type is what separates the two. Counting them as orphans would
-- report a defect against the feature working correctly — the same class of
-- mistake as reading an empty registry as "encryption not forced".
--
-- GUEST IS A ROW LIKE ANY OTHER AND A FINDING BY ITSELF. The user exists in
-- every database and is harmless while it cannot connect; CONNECT granted to it
-- opens the database to every login on the instance. It is called out in the
-- root object rather than left for a reader to spot in an array.
--
-- PERMISSIONS ARE SPLIT BY CLASS, AND THAT IS ABOUT BULK, NOT ABOUT
-- IMPORTANCE. Database-scoped grants — class 0, the ALTER ANY and CONTROL and
-- VIEW DEFINITION family — are listed in full, because the interesting ones are
-- rare and each is its own decision: a single CONTROL granted to an application
-- user is the whole finding, and a count would bury it. Object-scoped grants
-- are aggregated per principal and permission with a count, because a legacy
-- application can carry thousands of per-object and per-column grants that
-- would swamp the archive and answer nothing the aggregate does not.
--
-- Grants to public are the reason the object aggregate exists at all: a
-- permission held by public is held by everyone who can reach the database,
-- and it is invisible in any per-user listing.
--
-- SQL Server 2012 is the floor. authentication_type arrived with 2012, so the
-- contained-user test is available across the whole supported range.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT DB_NAME()                                                  AS [database],
       CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at],
       (SELECT CAST(d.containment AS int) FROM sys.databases AS d
         WHERE d.database_id = DB_ID())                           AS [containment],
       (SELECT COUNT(*) FROM sys.database_principals AS p
         WHERE p.type IN ('S','U','G'))                           AS [counts.users],
       (SELECT COUNT(*) FROM sys.database_principals AS p
         WHERE p.type = 'R' AND p.is_fixed_role = 0)              AS [counts.user_defined_roles],
       /* Members of the two roles that can do anything in the database. Named
          in the root because an analysis reaching for "who owns this database"
          should not have to walk an array to count them. */
       (SELECT COUNT(*)
          FROM sys.database_role_members AS rm
          JOIN sys.database_principals   AS r ON r.principal_id = rm.role_principal_id
         WHERE r.name = 'db_owner')                               AS [counts.db_owner_members],
       (SELECT COUNT(*)
          FROM sys.database_role_members AS rm
          JOIN sys.database_principals   AS r ON r.principal_id = rm.role_principal_id
         WHERE r.name = 'db_datawriter')                          AS [counts.db_datawriter_members],
       /* A user whose SID matches no login, excluding the contained users for
          which that is the point rather than a defect. */
       (SELECT COUNT(*)
          FROM sys.database_principals AS p
         WHERE p.type IN ('S','U','G')
           AND p.sid IS NOT NULL
           AND p.authentication_type <> 2
           AND NOT EXISTS (SELECT 1 FROM sys.server_principals AS sp
                            WHERE sp.sid = p.sid))                AS [counts.orphaned_users],
       (SELECT COUNT(*) FROM sys.database_permissions AS dp
         WHERE dp.grantee_principal_id = 0)                       AS [counts.permissions_to_public],
       (SELECT COUNT(*) FROM sys.database_permissions AS dp
         WHERE dp.class = 0)                                      AS [counts.database_permissions],
       (SELECT COUNT(*) FROM sys.database_permissions AS dp
         WHERE dp.class <> 0)                                     AS [counts.object_permissions],
       /* guest can connect only where CONNECT has been granted to it, which is
          not the default and is a finding on a user database. */
       (SELECT CASE WHEN EXISTS (
                SELECT 1 FROM sys.database_permissions AS dp
                 WHERE dp.grantee_principal_id = 2
                   AND dp.permission_name = 'CONNECT'
                   AND dp.state = 'G') THEN 1 ELSE 0 END)         AS [guest.can_connect]
OPTION (RECOMPILE, MAXDOP 1);

/* type raw AND described, the idiom 010.principals.sql uses: 'S' means nothing
   to a reader, and type_desc alone loses the letter every other view keys on. */
SELECT p.name                                                     AS [name],
       p.principal_id                                             AS [principal_id],
       p.type                                                     AS [type],
       p.type_desc                                                AS [type_desc],
       p.authentication_type_desc                                 AS [authentication_type],
       p.default_schema_name                                      AS [default_schema],
       CAST(p.is_fixed_role AS int)                               AS [is_fixed_role],
       CONVERT(varchar(23), p.create_date, 126)                   AS [create_date],
       CONVERT(varchar(23), p.modify_date, 126)                   AS [modify_date],
       /* Derived, never the SID itself. NULL rather than 0 for a principal the
          test does not apply to — a role has no login to be orphaned from, and
          reporting 0 there would read as "checked, and fine". */
       CASE WHEN p.type NOT IN ('S','U','G') OR p.sid IS NULL THEN NULL
            WHEN p.authentication_type = 2 THEN 0
            WHEN EXISTS (SELECT 1 FROM sys.server_principals AS sp
                          WHERE sp.sid = p.sid) THEN 0
            ELSE 1 END                                            AS [is_orphaned]
FROM sys.database_principals AS p
ORDER BY p.type, p.name
OPTION (RECOMPILE, MAXDOP 1);

SELECT r.name                                                     AS [role],
       m.name                                                     AS [member],
       m.type_desc                                                AS [member_type],
       CAST(r.is_fixed_role AS int)                               AS [role_is_fixed]
FROM sys.database_role_members AS rm
JOIN sys.database_principals   AS r ON r.principal_id = rm.role_principal_id
JOIN sys.database_principals   AS m ON m.principal_id = rm.member_principal_id
ORDER BY r.name, m.name
OPTION (RECOMPILE, MAXDOP 1);

/* Database-scoped grants, in full. class 0 is the database itself. */
SELECT g.name                                                     AS [grantee],
       g.type_desc                                                AS [grantee_type],
       dp.permission_name                                         AS [permission],
       dp.state_desc                                              AS [state],
       ISNULL(b.name, '')                                         AS [granted_by]
FROM sys.database_permissions AS dp
JOIN sys.database_principals  AS g ON g.principal_id = dp.grantee_principal_id
LEFT JOIN sys.database_principals AS b ON b.principal_id = dp.grantor_principal_id
WHERE dp.class = 0
ORDER BY g.name, dp.permission_name
OPTION (RECOMPILE, MAXDOP 1);

/* Object-scoped grants, aggregated. The object names are not projected: a
   legacy application carries thousands of them, and the shape of the grant is
   what a least-privilege finding rests on. */
SELECT g.name                                                     AS [grantee],
       g.type_desc                                                AS [grantee_type],
       dp.class_desc                                              AS [class],
       dp.permission_name                                         AS [permission],
       dp.state_desc                                              AS [state],
       COUNT(*)                                                   AS [objects]
FROM sys.database_permissions AS dp
JOIN sys.database_principals  AS g ON g.principal_id = dp.grantee_principal_id
WHERE dp.class <> 0
GROUP BY g.name, g.type_desc, dp.class_desc, dp.permission_name, dp.state_desc
ORDER BY COUNT(*) DESC, g.name
OPTION (RECOMPILE, MAXDOP 1);
