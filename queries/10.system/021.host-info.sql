-- @scope:       instance
-- @resultsets:  root:object
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     30
--
-- The operating system the instance runs on, and which platform it is.
--
-- The archive said nothing about the OS. 020.host-services.sql reads
-- sys.dm_server_services and the instance's registry hive, which gives the
-- service accounts and the startup parameters and not the host. An audit needs
-- the host for three things it is routinely asked — whether it is still
-- supported, whether a known storage or scheduler fix applies, and whether the
-- memory configuration makes sense against the physical machine — and in
-- September 2026 all three were asked and the answer was that the collection
-- does not report it.
--
-- THE AXIS IS PLATFORM, NOT VERSION, AND THAT IS THE CORRECTION. Splitting
-- this by build was the first design and two measurements broke it.
--
-- sys.dm_os_host_info does not carry the columns sys.dm_os_windows_info
-- carries. It has host_platform, host_distribution, host_release,
-- host_service_pack_level, host_sku, os_language_version and host_architecture
-- — every one prefixed host_ — and selecting windows_release from it fails
-- with Msg 207. Two files projecting "the same" fields would have projected
-- two different key sets into two archives nobody could compare.
--
-- And sys.dm_os_windows_info ON LINUX DOES NOT FAIL. It returns one row of
-- windows_release = '', windows_service_pack_level = '', windows_sku = NULL,
-- os_language_version = 0. Microsoft documents its behaviour on a non-Windows
-- host as undefined; in practice undefined is a well-formed row of nothing,
-- which sits in an archive looking like a measurement. This corpus's bar is
-- that a wrong value is worse than none.
--
-- So: one file, guarded the way 20.databases/023.log-vlf.sql is guarded.
-- sys.dm_os_host_info when it is there, which is 2017 and later,
-- sys.dm_os_windows_info otherwise. Below 2017 there is no ambiguity left to
-- resolve — SQL Server on Linux began with 2017 — so an instance without
-- dm_os_host_info is on Windows by construction, and platform is filled in by
-- that deduction rather than left empty.
--
-- HOST_RELEASE IS A NUMBER AND IT IS NOT THE MARKETING NAME. 10.0 covers
-- Windows Server 2016, 2019, 2022 and 2025 alike, and Windows 10 and 11 with
-- them. The raw value is projected and MUST NOT be mapped to a product name:
-- the mapping needs the build number, which neither view exposes, and a wrong
-- product name in an audit is worse than a version number the reader looks up.
-- host_sku is projected as its integer for the same reason.
--
-- That limit is worth stating rather than hiding. This collector answers the
-- memory question and half of the supportability one: it says which Windows
-- family, not which release, so supportability still needs a build number from
-- somewhere else.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @source varchar(24) = 'none', @err int = 0, @msg nvarchar(2048) = N'';

DECLARE @host TABLE (
    [platform]           varchar(32)  NULL,
    [distribution]       nvarchar(256) NULL,
    [release]            nvarchar(256) NULL,
    [service_pack_level] nvarchar(256) NULL,
    [sku]                int          NULL,
    [language_version]   int          NULL,
    [architecture]       nvarchar(64) NULL);

IF OBJECT_ID(N'sys.dm_os_host_info') IS NOT NULL
BEGIN
    /* Deferred for the reason every guard in this corpus is deferred: a view
       that is absent is a compile-time error, which a TRY at this level cannot
       catch, and a runtime one inside sp_executesql, which it can. */
    BEGIN TRY
        INSERT INTO @host ([platform], [distribution], [release],
                           [service_pack_level], [sku], [language_version],
                           [architecture])
        EXEC sys.sp_executesql
            N'SELECT h.host_platform, h.host_distribution, h.host_release,
                     h.host_service_pack_level, h.host_sku,
                     h.os_language_version, h.host_architecture
              FROM sys.dm_os_host_info AS h OPTION (RECOMPILE, MAXDOP 1)';
        SET @source = 'dm_os_host_info';
    END TRY
    BEGIN CATCH
        SELECT @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH
END
ELSE
BEGIN
    /* Windows by construction, not by reading a column: this branch is only
       reached below 2017, and there was no other platform then. The three
       columns the newer view has and this one does not stay NULL, which is the
       honest value for a fact this build cannot report. */
    BEGIN TRY
        INSERT INTO @host ([platform], [release], [service_pack_level],
                           [sku], [language_version])
        EXEC sys.sp_executesql
            N'SELECT ''Windows'', w.windows_release,
                     w.windows_service_pack_level, w.windows_sku,
                     w.os_language_version
              FROM sys.dm_os_windows_info AS w OPTION (RECOMPILE, MAXDOP 1)';
        SET @source = 'dm_os_windows_info';
    END TRY
    BEGIN CATCH
        SELECT @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH
END

/* Emitted from the staging table through scalar subqueries, so the one row
   this result set declares is produced whatever happened above. */
SELECT CONVERT(varchar(23), SYSDATETIME(), 126)                 AS [collected_at],
       @source                                                  AS [source],
       @err                                                     AS [error_number],
       NULLIF(@msg, N'')                                        AS [error_message],
       (SELECT TOP 1 h.[platform] FROM @host AS h)              AS [platform],
       (SELECT TOP 1 h.[distribution] FROM @host AS h)          AS [host.distribution],
       (SELECT TOP 1 h.[release] FROM @host AS h)               AS [host.release],
       (SELECT TOP 1 h.[service_pack_level] FROM @host AS h)    AS [host.service_pack_level],
       (SELECT TOP 1 h.[sku] FROM @host AS h)                   AS [host.sku],
       (SELECT TOP 1 h.[language_version] FROM @host AS h)      AS [host.language_version],
       (SELECT TOP 1 h.[architecture] FROM @host AS h)          AS [host.architecture]
OPTION (RECOMPILE, MAXDOP 1);
