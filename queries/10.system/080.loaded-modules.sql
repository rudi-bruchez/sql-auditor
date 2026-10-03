-- @scope:       instance
-- @resultsets:  root:object, modules:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     30
--
-- The modules loaded in the SQL Server process that do not say they are
-- Microsoft's.
--
-- WHY THIS COLLECTOR EXISTS. Antivirus agents, endpoint protection, data
-- access drivers and encryption products put DLLs inside sqlservr.exe, and
-- Microsoft documents them as a cause of crashes, access violations and slow
-- or inconsistent performance (KB 2033238, "Performance and consistency
-- issues when certain modules or filter drivers are loaded"). sp_Blitz check
-- 179 matches sys.dm_os_loaded_modules against a list of known names. Nothing
-- in this archive read the view, so a finding that the agent of the host was
-- inside the engine could only come from asking somebody.
--
-- THE FILTER IS company <> 'Microsoft Corporation', NULL KEPT, AND NOTHING
-- MORE CLEVER. The known-module list belongs to the analysis, which can be
-- updated without a new collector, and a list in the query would hide the
-- module nobody has listed yet. What is kept is everything the version
-- resource does not attribute to Microsoft in English, so a module with no
-- version resource is listed, and so is a Microsoft module whose company is
-- localised. The root carries the total and the Microsoft count beside the
-- listed one, so an empty array reads as "every module says Microsoft" and
-- not as "the view was not read".
--
-- WHAT LINUX RETURNS, MEASURED, AND WHAT IT MEANS. On SQL Server 2025 CU7 on
-- Linux (17.0.4065.4) the view returned 150 rows, and they are the Windows
-- image the platform abstraction layer runs: sqlservr.exe, ntdll.dll,
-- KERNEL32.DLL, sqlmin.dll, under bare names with no path although the
-- documentation says name includes the full path. 136 said Microsoft
-- Corporation. 13 had a NULL company and a NULL description, among them
-- sqlpal.dll, DkDll.dll, ADVAPI32.dll and msxml3.dll, and one, sqlevn70.rll,
-- carried a Russian translation of Microsoft Corporation. So a Linux instance
-- with nothing foreign in it lists fourteen modules here, all of them
-- Microsoft's, and the analysis has to know that rather than read them as
-- findings. The Linux process's own shared libraries are not in the view at
-- all: whatever an agent injects into the host process on Linux is invisible
-- from T-SQL, so an empty answer on Linux is not evidence of a clean host.
-- And because the names carry no path there, sp_Blitz's patterns, which all
-- start with a backslash, cannot match on Linux whatever is loaded.
--
-- WINDOWS WAS NOT MEASURED. The lab instances are Linux containers. On Windows
-- the documentation gives full paths in name, and the rows a clean instance
-- returns with a company other than Microsoft Corporation are not known from
-- a measurement; a localised Windows will add its translated company names.
--
-- Capped at 500 rows, which is far beyond what a process loads, and the root
-- says how many there were so the cap can never pass for the population.
--
-- NO JUDGEMENT IS APPLIED. A module from a backup agent, a monitoring tool or
-- a linked-server provider is in the process because somebody put it there
-- on purpose, and whether it is one of the modules Microsoft names is the
-- analysis's question.
--
-- SQL Server 2012 is the floor. Every column projected exists there. Not
-- collected: base_address (an address in one process's lifetime, which says
-- nothing the next restart keeps), and the debug, patched and private build
-- flags, which were 0 on every row that had a version resource and NULL on
-- the others.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT
    CONVERT(varchar(23), SYSDATETIME(), 126)                    AS [collected_at],
    (SELECT COUNT(*) FROM sys.dm_os_loaded_modules)             AS [counts.loaded],
    (SELECT COUNT(*) FROM sys.dm_os_loaded_modules
      WHERE company = N'Microsoft Corporation')                 AS [counts.microsoft],
    (SELECT COUNT(*) FROM sys.dm_os_loaded_modules
      WHERE company IS NULL
         OR company <> N'Microsoft Corporation')                AS [counts.other],
    500                                                         AS [listing_cap]
OPTION (RECOMPILE, MAXDOP 1);

SELECT TOP (500)
    m.name                                                      AS [name],
    m.company                                                   AS [company],
    m.description                                               AS [description],
    m.file_version                                              AS [file_version],
    m.product_version                                           AS [product_version]
FROM sys.dm_os_loaded_modules AS m
WHERE m.company IS NULL
   OR m.company <> N'Microsoft Corporation'
ORDER BY m.company, m.name
OPTION (RECOMPILE, MAXDOP 1);
