-- @scope:       instance
-- @resultsets:  root:object, intervals:array, waits:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     120
--
-- What the engine has been telling itself about its own health, interval by
-- interval, out of the system_health ring buffer.
--
-- WHY THIS COLLECTOR EXISTS, AND WHY IT IS NOT PART OF 060.system-health.sql.
-- That file reads the same ring buffer and shreds three attributes out of every
-- event: name, timestamp and error number. It is right to: it answers "how many
-- deadlocks, over what window". But the ring holds more than an event log. Four
-- times per interval the engine runs sp_server_diagnostics against itself and
-- posts the whole result as an XML payload, and 060 discards every one of them.
-- Measured on SQL Server 2022 (16.0.4265.3): 1195 sp_server_diagnostics_
-- component_result events in the ring against 1494 scheduler-monitor ones, four
-- components over 300 rounds, covering 24 hours and 55 minutes at one round
-- every 300 seconds. The ring was 4 002 413 characters, all of which 060
-- already pulls into memory and throws away.
--
-- It is a separate file for the reason 20.databases/025.fragmentation.sql
-- states: a cancelled batch returns none of its result sets however well the
-- areas inside it were guarded. 060 is three cheap shreds of one attribute
-- each; this is twenty-odd shreds over four megabytes of XML, about five
-- seconds of CPU against a tenth of one, and it is the expensive read of the
-- pair. Together they would put the deadlock inventory at
-- the mercy of the XML parse. Apart, a timeout here costs this file only.
--
-- WHAT IT ANSWERS THAT NOTHING ELSE IN THIS ARCHIVE CAN.
--
-- Per-interval waits. 80.workload/010.wait-stats.sql reads sys.dm_os_wait_stats,
-- which is cumulative since the instance started, and says so in its own header.
-- A cumulative counter cannot distinguish a wait that happened steadily for a
-- month from one that happened for ninety seconds last Tuesday, and this corpus
-- collects in one pass with no sleep, so it cannot compute a delta of its own.
-- topWaits is a delta the engine already computed, per interval, and it carries
-- the average and the maximum wait time rather than only the total. It also
-- separates preemptive from non-preemptive waits, a distinction that appears
-- nowhere else here.
--
-- Engine incident counters. nonYieldingTasksReported, latchWarnings,
-- spinlockBackoffs, sickSpinlockTypeAfterAv, isAccessViolationOccurred,
-- writeAccessViolationCount, BadPagesDetected and BadPagesFixed are the engine's
-- own record of having been in trouble. 10.system/074.memory-health.sql answers
-- a neighbouring question but only on SQL Server 2025 and only over one hour;
-- sp_server_diagnostics predates our 2012 floor. BadPagesDetected in particular
-- is corruption evidence with no other source here: 070.suspect-pages.sql reads
-- what was written to msdb, which is a different and narrower record.
--
-- A CPU timeline. 043.cpu-neighbours.sql answers "how much of this machine's CPU
-- is not SQL Server" from RING_BUFFER_SCHEDULER_MONITOR, which holds roughly
-- four hours. systemCpuUtilization and sqlCpuUtilization here give the same
-- split over the whole system_health window, a day rather than an afternoon.
--
-- Long I/O. ioLatchTimeouts is the 845 family and totalLongIos is the 833
-- family, both per interval. 030.file-io.sql gives cumulative latency per file;
-- neither it nor anything else says when the long I/Os happened.
--
-- NO STATEMENT TEXT IS COLLECTED, AND THAT RULES OUT ONE COMPONENT ON PURPOSE.
-- The queryProcessing payload carries a blockingTasks section holding full
-- blocked-process reports, and a blocked-process report holds process/inputbuf,
-- which is the verbatim SQL of the blocked and the blocking session. That is the
-- same disclosure that puts 061.deadlock-graphs.sql and 063.blocked-process-
-- reports.sql behind a flag and that 060 refuses outright. So this collector
-- counts the intervals in which the engine saw a blocking chain and stops there.
-- The count alone is worth having: sp_server_diagnostics posts these reports
-- whether or not "blocked process threshold (s)" is configured and whether or
-- not any Extended Events session subscribes to them, which is the double
-- condition 063 describes as blocking. An instance with neither still has a
-- blocking history here.
--
-- THE SHAPE OF THE PAYLOAD, MEASURED RATHER THAN ASSUMED. Each event carries
-- three data fields: component (text), state (text) and data (value, an XML
-- document). The component is one of IO_SUBSYSTEM, QUERY_PROCESSING, RESOURCE or
-- SYSTEM, and the root element of its payload is ioSubsystem, queryProcessing,
-- resource or system respectively. The state field is named state, not
-- state_desc: state_desc is the name of the Extended Events field in the event
-- definition, and reading it out of the ring buffer returns NULL in silence.
-- Verified on 16.0.4265.3 and 17.0.4065.4.
--
-- THE FOUR COMPONENTS OF ONE ROUND DO NOT SHARE A TIMESTAMP, AND THE FIRST
-- VERSION OF THIS FILE ASSUMED THEY DID. Each component is posted as its own
-- event with its own @timestamp, and the four differ. The first version joined
-- them on equality and produced 460 half-empty rows out of 1195 events, half of
-- them carrying only the SYSTEM columns and half only the others, which looked
-- plausible enough in a report to have shipped. Measured on both versions: the
-- spread inside one round is at most 1 millisecond, and truncating the timestamp
-- to the second groups the four correctly, 298 buckets of exactly four on
-- 16.0.4265.3 and 299 on 17.0.4065.4. The one or two buckets holding fewer are
-- the edges of the ring, where the oldest round was partly overwritten. That is
-- why components is projected per row: a row of three is a real edge case and
-- not a parse failure, and nothing here should have to guess which.
--
-- THE TIMESTAMPS HERE ARE UTC, like everything else in this ring buffer and
-- unlike everything else in this archive. 060's header explains why at length;
-- the same applies and instance_start is projected here in local time for the
-- same reason.
--
-- WHY THE INTERVALS ARE NOT SUMMED INTO ONE ROW. An interval is five minutes by
-- default and the window is about a day, so the array is a few hundred rows of
-- integers, which is cheap to carry and impossible to reconstruct later. A
-- single spike of spinlock backoffs at 03:40 and a steady trickle over a day
-- produce the same total and mean entirely different things.
--
-- NO JUDGEMENT IS APPLIED. A non-zero spinlockBackoffs is normal; the engine
-- backs off spinlocks continuously on a busy instance. What makes it a finding
-- is its shape over time against the rest of the archive, which is the reader's
-- work, not this file's.
--
-- SQL Server 2012 is the floor. sp_server_diagnostics and its system_health
-- event both predate it. Not collected for that reason:
--   the blockingTasks payload           (statement text, see above)
--   the resource component's full memoryReport (about forty entries per
--     interval; the four that carry a decision are projected and the rest
--     duplicate what 010.properties.sql and 015.buffer-pool.sql already read
--     from the DMVs, at one snapshot instead of three hundred)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

DECLARE @err_diag int = 0, @msg nvarchar(2048) = N'';

DECLARE @ring xml =
    (SELECT CAST(t.target_data AS xml)
       FROM sys.dm_xe_session_targets AS t
       JOIN sys.dm_xe_sessions AS s ON s.address = t.event_session_address
      WHERE s.name = 'system_health' AND t.target_name = 'ring_buffer');

DECLARE @intervals TABLE (
    [event_time]                datetime2(0) PRIMARY KEY,
    [components]                int           NULL,
    [states]                    varchar(120)  NULL,
    [system_cpu_pct]            int           NULL,
    [sql_cpu_pct]               int           NULL,
    [spinlock_backoffs]         bigint        NULL,
    [sick_spinlock_after_av]    varchar(60)   NULL,
    [latch_warnings]            int           NULL,
    [non_yielding_tasks]        int           NULL,
    [access_violation]          int           NULL,
    [write_av_count]            int           NULL,
    [dump_requests_interval]    int           NULL,
    [bad_pages_detected]        int           NULL,
    [bad_pages_fixed]           int           NULL,
    [io_latch_timeouts]         int           NULL,
    [long_ios_interval]         int           NULL,
    [long_ios_total]            int           NULL,
    [pending_io_requests]       int           NULL,
    [blocking_chains]           int           NULL,
    [available_physical_bytes]  bigint        NULL,
    [available_paging_bytes]    bigint        NULL,
    [working_set_bytes]         bigint        NULL,
    [page_alloc_potential]      bigint        NULL
);

DECLARE @waits TABLE (
    [list]          varchar(30),
    [wait_type]     varchar(60),
    [intervals]     int,
    [waits]         bigint,
    [avg_ms_max]    bigint,
    [max_ms]        bigint
);

BEGIN TRY
    /* WHAT MADE THIS FILE FAST WAS NOT WHAT I THOUGHT TWICE, AND THE TWO WRONG
       ANSWERS ARE WORTH MORE THAN THE RIGHT ONE.

       The pivot cost 34 814 ms of CPU on 16.0.4265.3 against 1 624 ms on
       17.0.4065.4 for the same 1195 events. The first guess blamed a MAX over
       an XML payload cast to nvarchar(max), which is aggregating a LOB;
       removing it changed nothing. The second blamed storing the payload in a
       table variable at all, so the shred was moved to read straight from the
       ring; that made it worse, and one form of it did not finish inside two
       minutes on either engine.

       Measured in isolation, every piece was cheap and the two engines agreed:
       reading the ring 170 ms, enumerating the event nodes 88 ms, three scalar
       values over 1195 events 455 ms, extracting the payload 251 ms. So the
       cost was never in any one operation. It was in their number.

       .value() with a PATH re-walks the document from the bound node on every
       call, and twenty-two of them per event is twenty-two walks. nodes() binds
       a node once, and .value() with a bare attribute off that node is a
       lookup. The CROSS APPLY below binds each component's payload root once
       by enumerating the children of the value element, and the twenty-two
       reads that follow are all bare attributes. Same output: 3 021 ms and 3 252 ms, the two engines now
       within ten percent of each other instead of a factor of twenty.

       The same mistake had a second form. Selecting the wait elements by one
       long nodes() path from the document root forced a reverse axis,
       ../../../../../@timestamp, to recover the round; that is what did not
       finish. Descending from the event by CROSS APPLY hands the timestamp over
       for free, which is why the waits query below is shaped as it is.

       The aggregation needs no CASE on the component. One event carries one
       component, so twenty of the twenty-two columns are NULL on any given row,
       and MAX over the bucket picks out the one value each column has. A
       component absent from a round leaves its columns NULL rather than zero,
       so a reader can tell "reported healthy" from "did not report". */
    INSERT INTO @intervals ([event_time], [components], [states],
        [system_cpu_pct], [sql_cpu_pct], [spinlock_backoffs],
        [sick_spinlock_after_av], [latch_warnings], [non_yielding_tasks],
        [access_violation], [write_av_count], [dump_requests_interval],
        [bad_pages_detected], [bad_pages_fixed],
        [io_latch_timeouts], [long_ios_interval], [long_ios_total],
        [pending_io_requests], [blocking_chains],
        [available_physical_bytes], [available_paging_bytes],
        [working_set_bytes], [page_alloc_potential])
    SELECT s.[bucket],
           COUNT(*),
           /* Every distinct state the round's components reported. CLEAN from
              all of them is the uneventful case; anything else is why this
              column exists. Built from MIN and MAX rather than a string
              aggregate so it runs on the 2012 floor, where STRING_AGG does not
              exist. Four components cannot report more than two distinct states
              without one of them being hidden here, which is a trade this
              column accepts: intervals_not_clean in the root counts the rounds,
              and the detail of a third state belongs to whoever goes to the
              instance. */
           CASE WHEN MIN(s.[state]) = MAX(s.[state]) THEN MIN(s.[state])
                ELSE MIN(s.[state]) + ',' + MAX(s.[state]) END,
           MAX(s.[system_cpu]),      MAX(s.[sql_cpu]),        MAX(s.[backoffs]),
           MAX(s.[sick_spinlock]),   MAX(s.[latch_warn]),     MAX(s.[non_yield]),
           MAX(s.[av]),              MAX(s.[write_av]),       MAX(s.[dumps]),
           MAX(s.[bad_detected]),    MAX(s.[bad_fixed]),
           MAX(s.[latch_timeouts]),  MAX(s.[long_io_int]),    MAX(s.[long_io_tot]),
           MAX(s.[pending_io]),      MAX(s.[blocking]),
           MAX(s.[avail_phys]),      MAX(s.[avail_page]),
           MAX(s.[working_set]),     MAX(s.[alloc_potential])
    FROM (
        SELECT CONVERT(datetime2(0), x.value('@timestamp', 'datetime2(3)'))  AS [bucket],
               x.value('(data[@name="state"]/text)[1]', 'varchar(30)')       AS [state],
               /* Every read below is a bare attribute off n, never a path.
                  That is the point: .value() with a path re-walks the document
                  from the bound node every time it is called, and twenty-two
                  of them per event is what cost 34 seconds. nodes() binds the
                  payload root once and an attribute read off it is a lookup.
                  An attribute that does not exist on this component's root
                  returns NULL, which is exactly what the aggregation wants. */
               n.value('@systemCpuUtilization', 'int')                       AS [system_cpu],
               n.value('@sqlCpuUtilization', 'int')                          AS [sql_cpu],
               n.value('@spinlockBackoffs', 'bigint')                        AS [backoffs],
               NULLIF(n.value('@sickSpinlockTypeAfterAv', 'varchar(60)'), 'none')
                                                                             AS [sick_spinlock],
               n.value('@latchWarnings', 'int')                              AS [latch_warn],
               n.value('@nonYieldingTasksReported', 'int')                   AS [non_yield],
               n.value('@isAccessViolationOccurred', 'int')                  AS [av],
               n.value('@writeAccessViolationCount', 'int')                  AS [write_av],
               n.value('@intervalDumpRequests', 'int')                       AS [dumps],
               n.value('@BadPagesDetected', 'int')                           AS [bad_detected],
               n.value('@BadPagesFixed', 'int')                              AS [bad_fixed],
               n.value('@ioLatchTimeouts', 'int')                            AS [latch_timeouts],
               n.value('@intervalLongIos', 'int')                            AS [long_io_int],
               n.value('@totalLongIos', 'int')                               AS [long_io_tot],
               /* The three counts below are paths, and they have to be: they
                  ask how many children exist. They run on one component each,
                  so a quarter of the rows, and they descend one or two levels
                  rather than searching. */
               CASE WHEN n.value('local-name(.)', 'varchar(30)') = 'ioSubsystem'
                    THEN n.value('count(longestPendingRequests/pendingRequest)', 'int')
               END                                                           AS [pending_io],
               -- Counted, never read. See the header: a blocked-process report
               -- carries verbatim SQL.
               CASE WHEN n.value('local-name(.)', 'varchar(30)') = 'queryProcessing'
                    THEN n.value('count(blockingTasks/blocked-process-report)', 'int')
               END                                                           AS [blocking],
               /* The memoryReport is a list of entry elements keyed by a
                  description attribute, not a set of attributes, so each value
                  is addressed by name and these four cannot avoid a predicate.
                  Reading them by position is what the SSRS reports shipped
                  beside this feature do, and that breaks the day Microsoft
                  inserts an entry. */
               n.value('(memoryReport/entry[@description="Available Physical Memory"]/@value)[1]', 'bigint') AS [avail_phys],
               n.value('(memoryReport/entry[@description="Available Paging File"]/@value)[1]',     'bigint') AS [avail_page],
               n.value('(memoryReport/entry[@description="Working Set"]/@value)[1]',               'bigint') AS [working_set],
               n.value('(memoryReport/entry[@description="Page Alloc Potential"]/@value)[1]',      'bigint') AS [alloc_potential]
        FROM @ring.nodes(
            '/RingBufferTarget/event[@name="sp_server_diagnostics_component_result"]')
            AS e(x)
        CROSS APPLY x.nodes('data[@name="data"]/value/*') AS pl(n)
    ) AS s
    GROUP BY s.[bucket]
    OPTION (RECOMPILE, MAXDOP 1);

    /* The four topWaits lists, aggregated over the window. The list name is
       kept because the four are not four views of one number: byDuration ranks
       on time waited and byCount on how often, and preemptive waits are the
       engine calling out to something that is not SQL Server, which is a
       different diagnosis from waiting on itself. Summing them together would
       mix all four.

       Read as a CROSS APPLY from the event rather than by one nodes() path from
       the document root, and that choice is measured too. The single-path form
       has to reach back up for the round's timestamp, and a reverse axis over a
       four-megabyte document did not finish inside two minutes on either engine.
       Descending from the event costs one enumeration and hands the timestamp
       over for free. */
    INSERT INTO @waits ([list], [wait_type], [intervals], [waits], [avg_ms_max], [max_ms])
    SELECT z.[list], z.[wait_type], COUNT(DISTINCT z.[bucket]), SUM(z.[waits]),
           MAX(z.[avg_ms]), MAX(z.[max_ms])
    FROM (
        SELECT CONVERT(datetime2(0),
                       x.value('@timestamp', 'datetime2(3)'))        AS [bucket],
               w.n.value('local-name(../..)', 'varchar(20)') + '_'
                 + w.n.value('local-name(..)', 'varchar(20)')        AS [list],
               w.n.value('@waitType', 'varchar(60)')                 AS [wait_type],
               w.n.value('@waits', 'bigint')                         AS [waits],
               w.n.value('@averageWaitTime', 'bigint')               AS [avg_ms],
               w.n.value('@maxWaitTime', 'bigint')                   AS [max_ms]
        FROM @ring.nodes(
            '/RingBufferTarget/event[@name="sp_server_diagnostics_component_result"]')
            AS e(x)
        CROSS APPLY x.nodes(
            'data[@name="data"]/value/queryProcessing/topWaits/*/*/wait') AS w(n)
    ) AS z
    WHERE z.[wait_type] IS NOT NULL
    GROUP BY z.[list], z.[wait_type]
    OPTION (RECOMPILE, MAXDOP 1);
END TRY
BEGIN CATCH
    SELECT @err_diag = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

SELECT
    CAST(CASE WHEN @ring IS NULL THEN 0 ELSE 1 END AS bit)      AS [session.running],
    (SELECT SUM([components]) FROM @intervals)                  AS [session.component_results],
    (SELECT COUNT(*) FROM @intervals)                           AS [session.intervals],
    -- How far back the ring reaches for THIS event. It is not the same window
    -- as 060 reports: the ring is shared, and a burst of errors or deadlocks
    -- pushes the oldest diagnostics out while leaving the event count high.
    CONVERT(varchar(23), (SELECT MIN([event_time]) FROM @intervals), 126)
                                                                AS [session.earliest_interval],
    CONVERT(varchar(23), (SELECT MAX([event_time]) FROM @intervals), 126)
                                                                AS [session.latest_interval],
    DATEDIFF(minute, (SELECT MIN([event_time]) FROM @intervals),
                     (SELECT MAX([event_time]) FROM @intervals)) AS [session.window_minutes],
    -- The nominal gap between two intervals, from the data rather than from the
    -- documented default, because a busy engine posts late.
    (SELECT CAST(AVG(CAST(g AS DECIMAL(9,1))) AS DECIMAL(9,1)) FROM
        (SELECT DATEDIFF(second, LAG([event_time]) OVER (ORDER BY [event_time]),
                                 [event_time]) AS g
           FROM @intervals) AS d WHERE g IS NOT NULL)            AS [session.median_gap_seconds],
    (SELECT COUNT(*) FROM @intervals WHERE [states] <> 'CLEAN')  AS [session.intervals_not_clean],
    (SELECT COUNT(*) FROM @intervals WHERE [blocking_chains] > 0) AS [session.intervals_with_blocking],
    CONVERT(varchar(23), (SELECT sqlserver_start_time FROM sys.dm_os_sys_info), 126)
                                                                AS [session.instance_start],
    CASE WHEN @err_diag = 0 THEN 1 ELSE 0 END                   AS [collected.diagnostics],
    @err_diag                                                   AS [errors.diagnostics],
    NULLIF(@msg, N'')                                           AS [error_message]
OPTION (RECOMPILE, MAXDOP 1);

/* One row per interval, newest first. The cap is generous because the rows are
   integers and the window is the point: three hundred rows of twenty integers
   is smaller than one execution plan. */
SELECT TOP (400)
    CONVERT(varchar(23), i.[event_time], 126)                   AS [at_utc],
    -- Four is a whole round. Fewer is an edge of the ring, not a parse failure.
    i.[components]                                              AS [components],
    i.[states]                                                  AS [states],
    i.[system_cpu_pct]                                          AS [system_cpu_pct],
    i.[sql_cpu_pct]                                             AS [sql_cpu_pct],
    i.[spinlock_backoffs]                                       AS [spinlock_backoffs],
    i.[sick_spinlock_after_av]                                  AS [sick_spinlock_after_av],
    i.[latch_warnings]                                          AS [latch_warnings],
    i.[non_yielding_tasks]                                      AS [non_yielding_tasks],
    i.[access_violation]                                        AS [access_violation],
    i.[write_av_count]                                          AS [write_av_count],
    i.[dump_requests_interval]                                  AS [dump_requests],
    i.[bad_pages_detected]                                      AS [bad_pages_detected],
    i.[bad_pages_fixed]                                         AS [bad_pages_fixed],
    i.[io_latch_timeouts]                                       AS [io_latch_timeouts],
    i.[long_ios_interval]                                       AS [long_ios_interval],
    i.[long_ios_total]                                          AS [long_ios_cumulative],
    i.[pending_io_requests]                                     AS [pending_io_requests],
    i.[blocking_chains]                                         AS [blocking_chains],
    i.[available_physical_bytes] / 1048576                       AS [available_physical_mb],
    i.[available_paging_bytes] / 1048576                         AS [available_paging_mb],
    i.[working_set_bytes] / 1048576                              AS [working_set_mb],
    i.[page_alloc_potential] / 1048576                           AS [page_alloc_potential_mb]
FROM @intervals AS i
ORDER BY i.[event_time] DESC
OPTION (RECOMPILE, MAXDOP 1);

/* The waits, aggregated. intervals says in how many of the window's intervals
   this type made the engine's own top ten, which is the figure a cumulative
   counter cannot give: a type present in 290 of 299 intervals is the shape of
   the workload, and one present in 3 is an incident. */
SELECT
    w.[list]                                                    AS [list],
    w.[wait_type]                                               AS [wait_type],
    w.[intervals]                                               AS [intervals],
    w.[waits]                                                   AS [waits],
    w.[avg_ms_max]                                              AS [worst_interval_avg_ms],
    w.[max_ms]                                                  AS [longest_single_wait_ms]
FROM @waits AS w
ORDER BY w.[list], w.[waits] DESC
OPTION (RECOMPILE, MAXDOP 1);
