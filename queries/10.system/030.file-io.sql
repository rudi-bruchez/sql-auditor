-- @scope:       instance
-- @resultsets:  root:object, volumes:array, files:array
-- @permissions: CONNECT, VIEW SERVER STATE, VIEW ANY DEFINITION
-- @timeout:     60
--
-- Per-file and per-volume I/O counters and latency, for every database.
--
-- Why this collector exists: the corpus already measured latency, but only for
-- tempdb — 050.tempdb.sql calls dm_io_virtual_file_stats(2, NULL) with the
-- database id hardcoded. On a real audit that block reported a believable
-- 44 ms on tempdb while a single index file on another volume was averaging
-- 115 ms of write latency and had accumulated 1.37 million hours of write
-- stall. The instance-wide picture was invisible, and the one number that was
-- collected made the storage look merely mediocre.
--
-- The volume comes from sys.dm_os_volume_stats, never from the physical path.
-- LEFT(physical_name, 3) is a Windows drive-letter heuristic: it yields "/va"
-- for every default path on Linux, which makes every file look co-located.
-- dm_os_volume_stats returns the real mount point on both platforms and
-- carries free space with it.
--
-- Latency is emitted as an average AND as its two raw components. The average
-- is per operation, not per unit of time, so idle periods neither dilute nor
-- inflate it — a file that did nothing for a month contributes nothing. But an
-- average over the whole uptime cannot separate a bad hour from a bad quarter,
-- so io_stall_*_ms and num_of_* travel with it: two collections taken apart
-- can be differenced to get the latency of the interval between them, which is
-- the only way to measure the current regime.
--
-- The counters reset when the instance restarts AND when a database is closed,
-- detached or taken offline — so a low count can mean a quiet file or a
-- recently reattached one. seconds_since_instance_start bounds the period from
-- above; it is not a measurement of it.
--
-- Durations use DATEDIFF(second, ...). DATEDIFF(millisecond, ...) overflows a
-- 32-bit int after about 24 days, so it fails on exactly the long-uptime
-- servers whose counters are worth collecting.
--
-- SQL Server 2012 is the floor. sys.dm_io_virtual_file_stats and
-- sys.dm_os_volume_stats both predate it. Not collected for that reason:
--   sys.dm_io_virtual_file_stats.io_stall_queued_read_ms   (2012 SP1, Azure)
--   sys.master_files.is_persistent_log_buffer              (2019)

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

SELECT si.sqlserver_start_time                                    AS [instance_start],
       DATEDIFF(second, si.sqlserver_start_time, GETDATE())       AS [seconds_since_instance_start],
       SYSDATETIME()                                              AS [collected_at],
       (SELECT COUNT(*) FROM sys.dm_io_virtual_file_stats(NULL, NULL)) AS [files_reporting],
       (SELECT SUM(fs.num_of_reads)  FROM sys.dm_io_virtual_file_stats(NULL, NULL) AS fs) AS [total.reads],
       (SELECT SUM(fs.num_of_writes) FROM sys.dm_io_virtual_file_stats(NULL, NULL) AS fs) AS [total.writes],
       (SELECT CAST(SUM(fs.num_of_bytes_read)    / 1073741824.0 AS DECIMAL(18,1))
        FROM sys.dm_io_virtual_file_stats(NULL, NULL) AS fs)      AS [total.gb_read],
       (SELECT CAST(SUM(fs.num_of_bytes_written) / 1073741824.0 AS DECIMAL(18,1))
        FROM sys.dm_io_virtual_file_stats(NULL, NULL) AS fs)      AS [total.gb_written]
FROM sys.dm_os_sys_info AS si
OPTION (RECOMPILE, MAXDOP 1);

/* Per volume. The weighted latency is total stall over total operations, not
   the mean of the per-file averages — a busy file and an idle one do not carry
   equal weight, and averaging averages would let one quiet file hide a loud
   one. */
SELECT vs.volume_mount_point                                      AS [mount_point],
       MIN(vs.logical_volume_name)                                AS [label],
       CAST(MIN(vs.total_bytes)     / 1073741824.0 AS DECIMAL(18,1)) AS [size.total_gb],
       CAST(MIN(vs.available_bytes) / 1073741824.0 AS DECIMAL(18,1)) AS [size.available_gb],
       COUNT(*)                                                   AS [files],
       SUM(CASE WHEN mf.type = 0 THEN 1 ELSE 0 END)               AS [data_files],
       SUM(CASE WHEN mf.type = 1 THEN 1 ELSE 0 END)               AS [log_files],
       SUM(fs.num_of_reads)                                       AS [reads],
       SUM(fs.num_of_writes)                                      AS [writes],
       CAST(SUM(fs.num_of_bytes_read)    / 1073741824.0 AS DECIMAL(18,1)) AS [gb_read],
       CAST(SUM(fs.num_of_bytes_written) / 1073741824.0 AS DECIMAL(18,1)) AS [gb_written],
       CAST(SUM(fs.io_stall_read_ms)  * 1.0 / NULLIF(SUM(fs.num_of_reads), 0)  AS DECIMAL(10,1)) AS [latency.avg_read_ms],
       CAST(SUM(fs.io_stall_write_ms) * 1.0 / NULLIF(SUM(fs.num_of_writes), 0) AS DECIMAL(10,1)) AS [latency.avg_write_ms],
       SUM(fs.io_stall_read_ms)                                   AS [latency.total_read_stall_ms],
       SUM(fs.io_stall_write_ms)                                  AS [latency.total_write_stall_ms],
       -- The spread across the files on this volume, because the average above
       -- hides the only distinction that changes what to do about it.
       --
       -- A volume whose files all write at 180 ms is misconfigured: a disabled
       -- write cache or a degraded path penalises everything on it equally. A
       -- volume where the busy files write at 240 ms and the idle ones on the
       -- same mount write at 1.5 ms is not misconfigured — it is saturated, and
       -- the fix is capacity or separation, not settings.
       --
       -- Both produce the same volume average. This was read wrong on a real
       -- audit and only caught by opening the per-file set, which is exactly
       -- the kind of thing a roll-up should not require.
       CAST(MIN(fs.io_stall_write_ms * 1.0 / NULLIF(fs.num_of_writes, 0)) AS DECIMAL(10,1))
                                                                  AS [latency.min_file_write_ms],
       CAST(MAX(fs.io_stall_write_ms * 1.0 / NULLIF(fs.num_of_writes, 0)) AS DECIMAL(10,1))
                                                                  AS [latency.max_file_write_ms],
       CAST(MIN(fs.io_stall_read_ms * 1.0 / NULLIF(fs.num_of_reads, 0)) AS DECIMAL(10,1))
                                                                  AS [latency.min_file_read_ms],
       CAST(MAX(fs.io_stall_read_ms * 1.0 / NULLIF(fs.num_of_reads, 0)) AS DECIMAL(10,1))
                                                                  AS [latency.max_file_read_ms]
FROM       sys.dm_io_virtual_file_stats(NULL, NULL) AS fs
JOIN       sys.master_files AS mf
        ON mf.database_id = fs.database_id AND mf.file_id = fs.file_id
CROSS APPLY sys.dm_os_volume_stats(fs.database_id, fs.file_id) AS vs
GROUP BY vs.volume_mount_point
ORDER BY SUM(fs.io_stall_write_ms) + SUM(fs.io_stall_read_ms) DESC
OPTION (RECOMPILE, MAXDOP 1);

/* Per file. Ordered by total stall rather than by latency: a file averaging
   500 ms over nine operations is noise, and a file averaging 115 ms over
   forty-three billion is the finding. */
SELECT DB_NAME(fs.database_id)                                    AS [database],
       mf.name                                                    AS [logical_name],
       mf.type_desc                                               AS [type],
       vs.volume_mount_point                                      AS [mount_point],
       mf.physical_name                                           AS [physical_name],
       CAST(mf.size * 8.0 / 1024 AS DECIMAL(18,1))                AS [size_mb],
       fs.num_of_reads                                            AS [reads],
       fs.num_of_writes                                           AS [writes],
       CAST(fs.num_of_bytes_read    / 1073741824.0 AS DECIMAL(18,1)) AS [gb_read],
       CAST(fs.num_of_bytes_written / 1073741824.0 AS DECIMAL(18,1)) AS [gb_written],
       CAST(fs.io_stall_read_ms  * 1.0 / NULLIF(fs.num_of_reads, 0)  AS DECIMAL(10,1)) AS [latency.avg_read_ms],
       CAST(fs.io_stall_write_ms * 1.0 / NULLIF(fs.num_of_writes, 0) AS DECIMAL(10,1)) AS [latency.avg_write_ms],
       fs.io_stall_read_ms                                        AS [latency.read_stall_ms],
       fs.io_stall_write_ms                                       AS [latency.write_stall_ms]
FROM       sys.dm_io_virtual_file_stats(NULL, NULL) AS fs
JOIN       sys.master_files AS mf
        ON mf.database_id = fs.database_id AND mf.file_id = fs.file_id
OUTER APPLY sys.dm_os_volume_stats(fs.database_id, fs.file_id) AS vs
ORDER BY fs.io_stall_write_ms + fs.io_stall_read_ms DESC
OPTION (RECOMPILE, MAXDOP 1);
