-- @scope:       instance
-- @resultsets:  root:object, certificates:array, encrypted_databases:array, backup_encryptors:array
-- @permissions: CONNECT, VIEW ANY DEFINITION, VIEW SERVER SECURITY STATE, MSDB READ
-- @timeout:     60
--
-- The certificates in master, whether their private key was ever backed up,
-- and which of them a database or a backup cannot be restored without.
--
-- WHY THIS COLLECTOR EXISTS. A database encrypted with TDE carries its
-- encryption key in its own boot page, and that key is protected by a
-- certificate (or an asymmetric key) in master. A backup taken WITH
-- ENCRYPTION is protected the same way. Neither can be restored on another
-- instance unless the certificate goes there with its private key, and the
-- private key leaves an instance only through BACKUP CERTIFICATE ... WITH
-- PRIVATE KEY. sys.certificates records the date of the last such backup in
-- pvt_key_last_backup_date, and NULL there means it never happened on this
-- instance. The archive said whether a database was encrypted
-- (20.databases/010.all-databases tde) and nothing about the one fact that
-- decides whether the encryption can be survived: an instance lost with an
-- unexported certificate takes every encrypted backup with it.
--
-- THREE ARRAYS, JOINED BY THUMBPRINT. certificates lists every certificate in
-- master that is not one of the engine's own (their names start with ##, they
-- have no private key, and listing them would put seven rows of noise above
-- the one that matters). encrypted_databases is sys.dm_database_encryption_keys
-- resolved to the certificate or asymmetric key that protects each database
-- key, with that certificate's last private key backup on the same row, which
-- is the finding as sp_Blitz states it (checks 119 and 202). backup_encryptors
-- is msdb's backup history grouped by the encryptor that protected each
-- backup, resolved the same way; a thumbprint found in the history and no
-- longer in master is a backup whose key has left the instance, and the row
-- says so with a NULL name rather than disappearing from an inner join.
--
-- THE KEY'S BACKUP IS DATED, NOT VERIFIED. pvt_key_last_backup_date says that
-- BACKUP CERTIFICATE ran with a private key, on this instance. It does not say
-- where the files went, whether they still exist, or whether the password that
-- protects them is known to anyone. And a certificate recreated here from its
-- files carries NULL although a copy plainly exists: measured on SQL Server
-- 2025, a certificate backed up, dropped and recreated FROM FILE with its
-- private key went from a date back to NULL. So a date is evidence of a
-- procedure, and NULL is a question to ask rather than a proof of loss.
--
-- WHAT A LOGIN BUILT FROM THE GRANT SCRIPT SEES, MEASURED. sys.certificates in
-- master is filtered by metadata visibility, and VIEW ANY DEFINITION makes
-- every row visible, pvt_key_last_backup_date included. The database keys are
-- another matter. From SQL Server 2022, sys.dm_database_encryption_keys asks
-- for VIEW SERVER SECURITY STATE, which VIEW SERVER PERFORMANCE STATE does not
-- include, and a login holding only the latter is refused the view with Msg
-- 300, "VIEW SERVER SECURITY STATE permission was denied on object 'server'".
-- That is why this file declares VIEW SERVER SECURITY STATE rather than VIEW
-- SERVER STATE: check probes it with a read of the same view, and the grant
-- script grants it by that name from 2022 and as VIEW SERVER STATE before,
-- where the narrower permission does not exist. A login built from the script
-- reads the view; measured on SQL Server 2025, with exactly the server-level
-- grants the script wrote, encryption_keys.readable came back true. A login
-- the probe finds without it does not run this file at all, and the manifest
-- says why. VIEW SERVER STATE alone, or VIEW SERVER SECURITY STATE alone,
-- reads the view on the same instance.
--
-- The read stays guarded. A refusal the probe did not see (a DENY on the view
-- itself, a corpus run by an older binary whose preflight does not know this
-- permission) leaves encrypted_databases empty and says so in
-- encryption_keys.error_number and _message, rather than failing the whole
-- file and taking the certificate list with it. Measured on SQL Server 2025
-- with a login built from the grant script of the previous release, which
-- granted VIEW SERVER PERFORMANCE STATE only: every certificate came back with
-- its backup date, the backup history was read, and the key read failed with
-- Msg 300. The databases encrypted with TDE are still named by
-- 20.databases/010.all-databases from sys.databases, which needs no server
-- state; what is lost is which certificate protects each one.
--
-- TEMPDB IS LEFT OUT OF encrypted_databases. As soon as one database on the
-- instance is encrypted, the engine encrypts tempdb too, and tempdb is
-- recreated at every start and never restored, so its row carries no restore
-- question. That is the documentation's account and was not measured: turning
-- TDE on encrypts the lab's tempdb until the next restart, an instance-wide
-- change this corpus does not make on a shared instance. The other rows were
-- measured on SQL Server 2025 with a database key created and encryption left
-- off, which puts the row in sys.dm_database_encryption_keys with its encryptor
-- and touches nothing else. There encryptor_type read CERTIFICATE_OAEP_256 and
-- not the CERTIFICATE of the documentation, in the key view and in msdb alike,
-- so an analysis matches on the prefix. set_date read 1900-01-01 for a key that
-- encryption was never turned on with, and is projected as NULL in that case
-- rather than as a date. A backup encrypted with a certificate that was then
-- dropped came back in backup_encryptors with its thumbprint, its count and a
-- NULL encryptor.
--
-- THE BACKUP HISTORY IS READ WHOLE, NOT OVER A WINDOW. 60.backup/010.history
-- answers what happened in thirty days; the question here is whether any backup
-- msdb still knows of needs a key that is missing, and an old backup is the one
-- whose certificate may have been dropped since. The grouping is one pass over
-- backupset, the same table 010.history reads.
--
-- NO JUDGEMENT IS APPLIED. A certificate signing a module or protecting an
-- endpoint needs no private key backup for a database to be restored, and the
-- arrays that say what a certificate protects are what separates those from the
-- ones that do. sp_Blitz flags a backup older than thirty days; the date is
-- projected and the threshold stays in the analysis.
--
-- SQL Server 2012 is the floor. sys.dm_database_encryption_keys has
-- encryptor_type from 2012, and encryption_state_desc only from 2019, so the
-- description is computed from encryption_state. The backup encryptor columns
-- of msdb.dbo.backupset arrived with backup encryption in 2014, so that read
-- is guarded by their presence and backup_encryptors is empty below 2014,
-- which is the true answer there. Not collected:
--   the certificate itself, its public key, and anything that would let the
--     archive stand in for the backup it is checking
--   sys.symmetric_keys, the database master key in master: it protects the
--     certificates' private keys here, and a certificate recreated from its
--     files on another instance is protected by that instance's own key
--   certificates in user databases: they sign modules or protect column keys,
--     and neither TDE nor backup encryption can use them

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @dek_err int = 0, @dek_msg nvarchar(2048) = N'',
        @bak_err int = 0, @bak_msg nvarchar(2048) = N'',
        @bak_source varchar(24) = 'none';

DECLARE @dek TABLE (
    [database_id]          int           NOT NULL,
    [encryption_state]     int           NULL,
    [key_algorithm]        nvarchar(32)  NULL,
    [key_length]           int           NULL,
    [encryptor_thumbprint] varbinary(32) NULL,
    [encryptor_type]       nvarchar(32)  NULL,
    [percent_complete]     real          NULL,
    [create_date]          datetime      NULL,
    [regenerate_date]      datetime      NULL,
    [set_date]             datetime      NULL);

DECLARE @bak TABLE (
    [encryptor_thumbprint] varbinary(32) NOT NULL,
    [encryptor_type]       nvarchar(32)  NULL,
    [key_algorithm]        nvarchar(32)  NULL,
    [backups]              int           NOT NULL,
    [databases]            int           NOT NULL,
    [first_backup]         datetime      NULL,
    [last_backup]          datetime      NULL);

/* Deferred and caught, because the refusal is a runtime error on 2022 and
   later for a login that holds VIEW SERVER PERFORMANCE STATE only, which the
   preflight normally catches first: see the header. The certificate list
   below does not depend on it. */
BEGIN TRY
    INSERT INTO @dek ([database_id], [encryption_state], [key_algorithm],
                      [key_length], [encryptor_thumbprint], [encryptor_type],
                      [percent_complete], [create_date], [regenerate_date],
                      [set_date])
    EXEC sys.sp_executesql
        N'SELECT k.database_id, k.encryption_state, k.key_algorithm,
                 k.key_length, k.encryptor_thumbprint, k.encryptor_type,
                 k.percent_complete, k.create_date, k.regenerate_date,
                 k.set_date
          FROM sys.dm_database_encryption_keys AS k
          WHERE k.database_id <> 2
          OPTION (RECOMPILE, MAXDOP 1)';
END TRY
BEGIN CATCH
    SELECT @dek_err = ERROR_NUMBER(), @dek_msg = ERROR_MESSAGE();
END CATCH

/* Backup encryption is 2014 and later; the columns are looked for rather than
   a version compared, so the read follows what msdb actually holds. */
IF COL_LENGTH(N'msdb.dbo.backupset', N'encryptor_thumbprint') IS NOT NULL
BEGIN
    BEGIN TRY
        INSERT INTO @bak ([encryptor_thumbprint], [encryptor_type],
                          [key_algorithm], [backups], [databases],
                          [first_backup], [last_backup])
        EXEC sys.sp_executesql
            N'SELECT b.encryptor_thumbprint, MAX(b.encryptor_type),
                     MAX(b.key_algorithm), COUNT(*),
                     COUNT(DISTINCT b.database_name),
                     MIN(b.backup_finish_date), MAX(b.backup_finish_date)
              FROM msdb.dbo.backupset AS b
              WHERE b.encryptor_thumbprint IS NOT NULL
              GROUP BY b.encryptor_thumbprint
              OPTION (RECOMPILE, MAXDOP 1)';
        SET @bak_source = 'backupset';
    END TRY
    BEGIN CATCH
        SELECT @bak_err = ERROR_NUMBER(), @bak_msg = ERROR_MESSAGE();
    END CATCH
END
ELSE
    SET @bak_source = 'not_on_this_version';

SELECT
    CONVERT(varchar(23), SYSDATETIME(), 126)                    AS [collected_at],
    (SELECT COUNT(*) FROM master.sys.certificates
      WHERE name NOT LIKE N'##%')                               AS [counts.certificates],
    -- Only a certificate holding a private key has one to lose. One without
    -- (a public certificate imported to verify a signature) needs no backup.
    (SELECT COUNT(*) FROM master.sys.certificates
      WHERE name NOT LIKE N'##%'
        AND pvt_key_encryption_type <> 'NA')                    AS [counts.certificates_with_private_key],
    (SELECT COUNT(*) FROM master.sys.certificates
      WHERE name NOT LIKE N'##%'
        AND pvt_key_encryption_type <> 'NA'
        AND pvt_key_last_backup_date IS NULL)                   AS [counts.private_key_never_backed_up],
    (SELECT COUNT(*) FROM @dek)                                 AS [counts.database_keys],
    (SELECT COUNT(*) FROM @bak)                                 AS [counts.backup_encryptors],
    -- Whether the database key read ran. readable = 0 with a number is the
    -- 2022+ permission gap of the header, and then counts.database_keys = 0
    -- means "not allowed to look", not "no database is encrypted".
    CAST(CASE WHEN @dek_err = 0 THEN 1 ELSE 0 END AS bit)       AS [encryption_keys.readable],
    NULLIF(@dek_err, 0)                                         AS [encryption_keys.error_number],
    NULLIF(@dek_msg, N'')                                       AS [encryption_keys.error_message],
    @bak_source                                                 AS [backup_history.source],
    NULLIF(@bak_err, 0)                                         AS [backup_history.error_number],
    NULLIF(@bak_msg, N'')                                       AS [backup_history.error_message]
OPTION (RECOMPILE, MAXDOP 1);

/* Every certificate of master but the engine's own. The thumbprint is the
   join key of the two arrays below and of the backup history, and it is a
   hash of the public certificate, not a secret. */
SELECT
    c.name                                                      AS [name],
    c.subject                                                   AS [subject],
    CONVERT(varchar(42), c.thumbprint, 1)                       AS [thumbprint],
    c.pvt_key_encryption_type_desc                              AS [private_key],
    CONVERT(varchar(23), c.pvt_key_last_backup_date, 126)       AS [private_key_last_backup],
    CONVERT(varchar(23), c.start_date, 126)                     AS [valid_from],
    -- An expired certificate still decrypts a TDE database and still restores
    -- an encrypted backup; it is projected because creating a new encrypted
    -- backup with it raises a warning, and because rotation is a question.
    CONVERT(varchar(23), c.expiry_date, 126)                    AS [expires],
    c.key_length                                                AS [key_length],
    -- Created here or brought in from files: the catalogue does not say, and
    -- the issuer of a self-signed certificate is its subject either way.
    c.issuer_name                                               AS [issuer]
FROM master.sys.certificates AS c
WHERE c.name NOT LIKE N'##%'
ORDER BY c.name
OPTION (RECOMPILE, MAXDOP 1);

/* One row per database key, tempdb excepted. encryptor is NULL when the
   thumbprint matches nothing in master, which is a database whose key is
   protected by something this instance no longer holds. */
SELECT
    DB_NAME(k.[database_id])                                    AS [database],
    k.[encryption_state]                                        AS [encryption_state],
    CASE k.[encryption_state]
        WHEN 0 THEN 'NONE'
        WHEN 1 THEN 'UNENCRYPTED'
        WHEN 2 THEN 'ENCRYPTION_IN_PROGRESS'
        WHEN 3 THEN 'ENCRYPTED'
        WHEN 4 THEN 'KEY_CHANGE_IN_PROGRESS'
        WHEN 5 THEN 'DECRYPTION_IN_PROGRESS'
        WHEN 6 THEN 'PROTECTION_CHANGE_IN_PROGRESS'
    END                                                         AS [encryption_state_desc],
    k.[percent_complete]                                        AS [percent_complete],
    k.[key_algorithm]                                           AS [key_algorithm],
    k.[key_length]                                              AS [key_length],
    k.[encryptor_type]                                          AS [encryptor_type],
    CONVERT(varchar(42), k.[encryptor_thumbprint], 1)           AS [encryptor_thumbprint],
    COALESCE(c.name, a.name)                                    AS [encryptor],
    c.pvt_key_encryption_type_desc                              AS [private_key],
    -- NULL both when the private key was never backed up and when the
    -- encryptor is not a certificate of master: encryptor says which.
    CONVERT(varchar(23), c.pvt_key_last_backup_date, 126)       AS [private_key_last_backup],
    -- UTC, as the view records them.
    CONVERT(varchar(23), k.[create_date], 126)                  AS [key_created_utc],
    CONVERT(varchar(23), k.[regenerate_date], 126)              AS [key_regenerated_utc],
    -- 1900-01-01 when encryption was never turned on with this key.
    CONVERT(varchar(23), NULLIF(k.[set_date], '19000101'), 126) AS [key_set_utc]
FROM @dek AS k
LEFT JOIN master.sys.certificates AS c
       ON c.thumbprint = k.[encryptor_thumbprint]
LEFT JOIN master.sys.asymmetric_keys AS a
       ON a.thumbprint = k.[encryptor_thumbprint]
ORDER BY DB_NAME(k.[database_id])
OPTION (RECOMPILE, MAXDOP 1);

/* The backup history grouped by what encrypted it. A row whose encryptor is
   NULL names backups this instance can no longer decrypt by itself. */
SELECT
    CONVERT(varchar(42), b.[encryptor_thumbprint], 1)           AS [encryptor_thumbprint],
    b.[encryptor_type]                                          AS [encryptor_type],
    COALESCE(c.name, a.name)                                    AS [encryptor],
    c.pvt_key_encryption_type_desc                              AS [private_key],
    CONVERT(varchar(23), c.pvt_key_last_backup_date, 126)       AS [private_key_last_backup],
    b.[key_algorithm]                                           AS [key_algorithm],
    b.[backups]                                                 AS [backups],
    b.[databases]                                               AS [databases],
    CONVERT(varchar(23), b.[first_backup], 126)                 AS [first_backup],
    CONVERT(varchar(23), b.[last_backup], 126)                  AS [last_backup]
FROM @bak AS b
LEFT JOIN master.sys.certificates AS c
       ON c.thumbprint = b.[encryptor_thumbprint]
LEFT JOIN master.sys.asymmetric_keys AS a
       ON a.thumbprint = b.[encryptor_thumbprint]
ORDER BY b.[last_backup] DESC
OPTION (RECOMPILE, MAXDOP 1);
