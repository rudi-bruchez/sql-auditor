-- @scope:       instance
-- @resultsets:  root:object, accelerators:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     30
-- @min_version: 16
--
-- Integrated offload and acceleration: what the instance can hand to hardware,
-- and what it is actually doing.
--
-- Why this collector exists. SQL Server 2022 added Intel QuickAssist offload
-- for backup compression, and the interesting state is not "is the feature
-- available" but "was it configured and did it fall back". Three outcomes look
-- identical from outside: the accelerator was never enabled, it was enabled and
-- runs in hardware, or it was enabled and quietly runs in SOFTWARE because the
-- card is absent, the driver failed to load, or the edition does not allow it.
-- Only the third is a finding, and it is invisible everywhere else in the
-- archive — the backups keep working, a little slower and on the CPU that was
-- supposed to be freed.
--
-- mode_reason_desc is what separates the three, and it is the reason this file
-- projects it verbatim rather than deriving a verdict. Its values name the
-- cause: SOFTWARE_MODE_ACCELERATOR_HARDWARE_NOT_FOUND is a broken deployment,
-- SOFTWARE_MODE_NON_ENTERPRISE_SKU is a licensing decision, and
-- NONE_HARDWARE_OFFLOAD_NOT_ENABLED is the untouched default. Mapping them to
-- three words here would throw away the distinction the next release will
-- extend.
--
-- THE VIEW IS NEVER EMPTY ON A SUPPORTED BUILD. A row for QAT is present from
-- 2022 onwards whether or not the hardware exists and whether or not the driver
-- is installed, so an empty array is a collection failure and not an answer.
-- The count in the root object is what makes the two distinguishable, since
-- every other array in this corpus is empty only when something went wrong.
--
-- The gate is the bare major 16. The view arrived with 2022 RTM, not with a
-- cumulative update, so there is no build-level floor to respect the way
-- 20.databases/023.log-vlf.sql has to respect 13.0.5026. Future accelerators
-- add rows to this view, not columns, which is why the projection is explicit
-- and still safe.
--
-- Permissions: the documentation names VIEW PERFORMANCE STATE, one of the
-- narrow server permissions 2022 introduced. VIEW SERVER STATE covers it, and
-- the directive above declares the wide one because that is the vocabulary the
-- preflight probes and the grant script share. A login granted only the narrow
-- permission reads this view fine; the preflight will simply have nothing to
-- say about it.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT
    COUNT(*)                                                          AS [offload.reported],
    SUM(CASE WHEN a.mode_desc = 'HARDWARE' THEN 1 ELSE 0 END)         AS [offload.in_hardware],
    SUM(CASE WHEN a.mode_desc = 'SOFTWARE' THEN 1 ELSE 0 END)         AS [offload.in_software],
    SUM(CASE WHEN a.mode_desc = 'NONE'     THEN 1 ELSE 0 END)         AS [offload.off],
    SUM(CASE WHEN a.accelerator_hardware_detected = 1 THEN 1 ELSE 0 END)
                                                                      AS [offload.hardware_detected]
FROM sys.dm_server_accelerator_status AS a
OPTION (RECOMPILE, MAXDOP 1);

/* ───────── accelerators: one row per accelerator the build knows ───────── */
SELECT
    a.accelerator,
    a.accelerator_desc,
    a.mode_desc,
    a.mode_reason_desc,
    a.accelerator_hardware_detected,
    a.accelerator_library_version,
    a.accelerator_driver_version
FROM sys.dm_server_accelerator_status AS a
ORDER BY a.accelerator
OPTION (RECOMPILE, MAXDOP 1);
