-- @scope:       instance
-- @resultsets:  root:object, nodes:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     30
--
-- Processor topology, and whether the nodes SQL Server reports are hardware or
-- its own invention.
--
-- WHY THIS FILE IS SHAPED THE WAY IT IS. It used to report socket_count,
-- cores_per_socket and numa_node_count, while a second file reported the
-- soft-NUMA configuration. Both were correct and the split made them
-- unreadable: numa_node_count = 3 was taken for three hardware NUMA nodes, and
-- a client report went out recommending a MAXDOP based on memory locality that
-- does not exist on that machine. The soft-NUMA answer had been collected the
-- whole time, in the other file.
--
-- So the two are now one result set, and the derived answer is projected rather
-- than left to be inferred. A fact that is meaningless without its neighbour
-- does not get its own file.
--
-- WHAT THE DISTINCTION IS. Automatic soft-NUMA, on by default since SQL Server
-- 2016, partitions schedulers into groups of at most eight whenever a hardware
-- node carries more than eight logical processors. It creates SCHEDULER nodes.
-- They share the memory node they were carved out of.
--
-- sys.dm_os_sys_info.numa_node_count counts SQLOS nodes, so it counts the soft
-- ones too and cannot answer the question on its own. The decisive reading is
-- sys.dm_os_nodes.memory_node_id: when every scheduler node maps to the same
-- memory node, there is one hardware node and no remote memory access to
-- reason about. Measured on a virtual machine reporting three nodes: all three
-- carried memory_node_id 0, and sys.dm_os_memory_nodes held a single entry.
--
-- WHY IT CHANGES A RECOMMENDATION. On real hardware NUMA, capping MAXDOP at the
-- size of a node keeps a query's threads and their memory on the same node.
-- Under soft-NUMA the same cap is still reasonable — Microsoft's guidance for a
-- single node above eight logical processors is 8, and it keeps a parallel
-- query inside one scheduler node — but the reason is scheduler contention, not
-- memory locality. Same number, different argument, and an auditor who gives
-- the wrong argument gets corrected by the client's infrastructure team.
--
-- A VIRTUAL MACHINE CAN LIE ABOUT ALL OF IT. socket_count and cores_per_socket
-- are whatever the hypervisor presents, and a synthetic topology that does not
-- match the host makes every number here decorative. That question cannot be
-- answered from inside SQL Server, so the collector reports the facts and the
-- analysis layer is expected to say so.
--
-- SQL Server 2016 SP2 IS THE FLOOR, AND IT IS NOT softnuma_configuration THAT
-- SETS IT. Four columns of sys.dm_os_sys_info are documented as "SQL Server
-- 2016 (13.x) SP2 and later": socket_count, cores_per_socket, numa_node_count
-- and softnuma_configuration. They sit in the root SELECT, so on 2016 RTM or
-- SP1 the batch does not lose a column — it fails outright with an invalid
-- column name, and the collector produces nothing at all.
--
-- The floor said 13.0 until this was checked. That is one build family below
-- what the columns need, and the gap is invisible on any instance from SP2
-- upwards: the 2016 SP3 and 2017 instances this corpus has been run against
-- would never have shown it. Everything else here predates 2012.
--
-- THE EDITION'S PROCESSOR CEILING, AND WHY ITS SIGNATURE IS A VETO. An edition
-- that licenses fewer processors than the machine carries makes every count
-- above read as something it is not. The case that motivated this block: a
-- hypervisor presenting six single-core sockets, an edition capped at four
-- sockets, cpu_count 6 and scheduler_count 4. An analysis layer that computes a
-- MAXDOP from cpu_count recommends 6 on an instance that has four schedulers,
-- while the recommendation that would help is to fix the topology.
--
-- The two ceilings do not look alike, which is the whole difficulty. A LOGICAL
-- PROCESSOR ceiling applies before SQLOS starts: cpu_count is already the
-- granted number, the error log says "using N logical processors based on SQL
-- Server licensing", and no scheduler is offline. A test on
-- scheduler_count < cpu_count never sees it. A SOCKET ceiling applies after:
-- cpu_count carries the whole machine and the surplus schedulers sit in
-- VISIBLE OFFLINE. Both were measured on disposable instances on 22 September
-- 2026, the first on Express over a 22 processor host, the second on Standard
-- over six single-core sockets.
--
-- So edition_cap_signature names WHICH evidence spoke, and it is a veto rather
-- than a verdict. A positive value proves a ceiling. none_observed proves
-- nothing, because once a ceiling is applied the instance stops reporting the
-- size of the host it was cut down from: on that Express instance socket_count
-- came back as 0. A caller may refuse a recommendation on a positive signature
-- and may never conclude "no ceiling" from none_observed.
--
-- It is a string and not a bit on purpose. A flag would say that something is
-- wrong and erase the only part that is actionable, which is which of the three
-- readings fired.
--
-- sockets_over_limit is the only condition that PROVES a socket ceiling. The
-- first version of this test was scheduler_count < cpu_count with affinity
-- ruled out, and that does not prove it: a core ceiling satisfies it too, so
-- the signature would have named evidence it did not hold.
--
-- logical_processors_below_topology reads hyperthread_ratio, which counts the
-- logical processors of ONE socket. A single socket carrying more than the
-- whole instance uses proves a ceiling. The converse proves nothing, and on a
-- multi-socket host the test stays silent, which is the half that matters.
--
-- schedulers_offline_unexplained says processors are missing and that affinity
-- does not account for it. It does not say why, because nothing readable from
-- inside the instance says why, and inventing a cause is worse than reporting
-- the gap.
--
-- cpus_lost_at_least is named for what it is. On a multi-socket host the real
-- loss is larger, and the archive cannot know by how much.
--
-- THE LIMITS TABLE, AND THE TWO PAGES IT COMES FROM, read on 22 September 2026.
-- "SERVERPROPERTY (Transact-SQL)" gives the EditionID values. "Compute capacity
-- limits by edition of SQL Server" gives the limits, but it carries four rows
-- only: Enterprise at the core, Developer, Standard, Express. The Web, Business
-- Intelligence and Express with Advanced Services rows come from the "Editions
-- and supported features" pages of 2019 and earlier, and saying so is the
-- point: they are not the same source and they are not maintained alike.
--
-- A NULL limit means the operating system maximum, never "unknown". Unknown is
-- what unknown_edition exists to say, and Azure SQL Edge lands there on
-- purpose: it is out of scope for this table.
--
-- Two readings the table does not make, and that must not be made from it. In a
-- virtual machine the limit applies to logical processors and not to cores. And
-- edition_core_limit counts cores while cpu_count counts logical processors, so
-- 48 schedulers under a 24 core limit is not an anomaly on a hyperthreaded
-- machine. Neither derived field compares those two natures.
--
-- Only the Standard row carries a version condition, and it is written
-- CONVERT(int, SERVERPROPERTY('ProductMajorVersion')) >= 17. The conversion is
-- not cosmetic: SERVERPROPERTY returns a sql_variant whose base type is
-- nvarchar, so the comparison without it answers false on a version 17
-- instance. Measured twice on a 17.0 build, which returned false unconverted
-- and true converted. It is the one line of the table that would have broken
-- silently, on a plausible-looking number.
--
-- The table itself is verified against documentation and against nothing else.
-- One instance knows one edition, so no run of this collector and no continuous
-- integration matrix can exercise more than the row it happens to sit on.
--
-- WHAT maxdop_guidance COUNTS, AND THAT IT CHANGED TWICE. It used to be
-- computed from cpu_count, which is the count this whole block exists to
-- distrust: on a capped instance it recommended a MAXDOP over processors the
-- instance is not allowed to use.
--
-- It was then rewritten as "the smallest node, capped at eight", and that was
-- still wrong, on a second count. Eight is the SQL Server 2008 to 2014 table.
-- The table in force since 2016, on "Server configuration: max degree of
-- parallelism", section Recommendations, has four rows, read here with p the
-- online schedulers of a node and n the number of nodes:
--
--   n = 1 and p <= 8    the guidance is p
--   n = 1 and p >  8    the guidance is 8
--   n > 1 and p <= 16   the guidance is p
--   n > 1 and p >  16   the guidance is p / 2, never above 16
--
-- WHICH NODES n COUNTS is the question that decides the answer, and the page
-- answers it in a sentence under the table: "NUMA node in the previous table
-- refers to soft-NUMA nodes automatically created by SQL Server 2016 (13.x) and
-- higher versions, or hardware-based NUMA nodes if soft-NUMA is disabled." So n
-- is the SQLOS node count, soft nodes included, and not the hardware node count
-- that soft_numa_in_effect and memory_node_count exist to tell apart. That is
-- not an oversight by Microsoft: the table's stated purpose is to keep every
-- worker thread of a parallel query inside one soft-NUMA node, and a soft node
-- is the unit that serves that purpose.
--
-- It matters here more than on most instances. Read as hardware nodes, this
-- machine has one node of 22 and the guidance is 8; read as SQLOS nodes it has
-- two of 11 and the guidance is 11. The wrong reading is not a rounding
-- difference, and the two readings are both available in this same result set,
-- which is how the wrong one gets taken.
--
-- p is the MINIMUM online_scheduler_count rather than the maximum, because on
-- asymmetric nodes of four and eight the maximum hands out a recommendation of
-- eight, twice what the smaller node can carry. maxdop_guidance_basis names the
-- row that answered, so a value of 8 is never ambiguous between the single-node
-- cap and a node that simply holds eight schedulers.

-- @min_version: 13.0.5026

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT
    si.cpu_count                                    AS [processors.logical],
    si.socket_count                                 AS [processors.sockets],
    si.cores_per_socket                             AS [processors.cores_per_socket],
    si.hyperthread_ratio                            AS [processors.hyperthread_ratio],
    si.scheduler_count                              AS [processors.schedulers],

    -- The direct fact behind a socket ceiling, and behind an affinity mask,
    -- which is why the affinity setting is projected beside it rather than left
    -- in another file: whoever reads one has to read the other in the same
    -- breath or they will call a deliberate restriction a licensing problem.
    sch.schedulers_offline                          AS [processors.schedulers_offline],
    si.affinity_type_desc                           AS [processors.affinity_type],

    -- NULL is the operating system maximum, not "we do not know".
    lim.socket_limit                                AS [processors.edition_socket_limit],
    lim.core_limit                                  AS [processors.edition_core_limit],
    cap.signature                                   AS [processors.edition_cap_signature],
    CASE cap.signature
        WHEN 'sockets_over_limit'                THEN si.cpu_count - si.scheduler_count
        WHEN 'schedulers_offline_unexplained'    THEN si.cpu_count - si.scheduler_count
        WHEN 'logical_processors_below_topology' THEN si.hyperthread_ratio - si.cpu_count
        ELSE 0 END                                  AS [processors.cpus_lost_at_least],

    -- The counts live under [numa.…], not [nodes.…], and the name is load
    -- bearing: the second result set is called "nodes", the encoder builds its
    -- nested objects from these dotted prefixes, and a prefix that matches a
    -- result-set name makes the same key an object and an array at once. The
    -- encoder refuses that, so the collector produced nothing at all until the
    -- prefix moved. "numa" is also the truer name — these are counts about
    -- NUMA, while "nodes" is the per-node evidence beneath them.
    --
    -- What SQLOS calls NUMA nodes. This counts soft nodes as well, which is
    -- exactly why it must never be read alone.
    si.numa_node_count                              AS [numa.sqlos_node_count],
    si.softnuma_configuration                       AS [numa.softnuma_configuration],
    si.softnuma_configuration_desc                  AS [numa.softnuma_configuration_desc],

    -- The hardware answer, and it has to be asked carefully.
    --
    -- Counting rows in sys.dm_os_memory_nodes is the obvious way and it is
    -- wrong. The first version of this file did that and reported two memory
    -- nodes on a machine that has one: the instance carries a node 1 with no
    -- memory reserved, nothing committed and a cpu_affinity_mask of zero, a
    -- placeholder no scheduler belongs to. The derived flag went true and
    -- claimed hardware NUMA on the very instance that motivated this file.
    --
    -- The count that means something is the number of distinct memory nodes
    -- the scheduler nodes actually sit on. A phantom node has no schedulers,
    -- so it cannot inflate it. Both are projected: when they differ, the
    -- difference is a fact about the instance rather than something to hide.
    (SELECT COUNT(DISTINCT n.memory_node_id) FROM sys.dm_os_nodes AS n
      WHERE n.node_state_desc NOT LIKE '%DAC%')     AS [numa.memory_node_count],
    (SELECT COUNT(*) FROM sys.dm_os_memory_nodes
      WHERE memory_node_id <> 64)                   AS [numa.memory_nodes_reported],
    (SELECT COUNT(*) FROM sys.dm_os_nodes
      WHERE node_state_desc NOT LIKE '%DAC%')       AS [numa.scheduler_node_count],

    -- The conclusion, computed here so nobody has to reach it twice. True means
    -- the reported nodes are scheduler groups over a single memory node, so
    -- memory locality is not in play.
    CONVERT(bit, CASE
        WHEN (SELECT COUNT(*) FROM sys.dm_os_nodes
               WHERE node_state_desc NOT LIKE '%DAC%')
           > (SELECT COUNT(DISTINCT n.memory_node_id) FROM sys.dm_os_nodes AS n
               WHERE n.node_state_desc NOT LIKE '%DAC%')
        THEN 1 ELSE 0 END)                          AS [numa.soft_numa_in_effect],
    CONVERT(bit, CASE
        WHEN (SELECT COUNT(DISTINCT n.memory_node_id) FROM sys.dm_os_nodes AS n
               WHERE n.node_state_desc NOT LIKE '%DAC%') > 1
        THEN 1 ELSE 0 END)                          AS [numa.hardware_numa_present],

    -- The value Microsoft's guidance lands on, for the analysis layer to
    -- compare against what is configured. The four rules are the header's, and
    -- the basis says which of them answered, so a reader never has to guess
    -- whether a value of 8 is the single-node cap or a node that happens to
    -- carry eight schedulers.
    CASE dop.basis
        WHEN 'logical_processors_fallback'
             THEN CASE WHEN si.cpu_count <= 8 THEN si.cpu_count ELSE 8 END
        WHEN 'single_node_le_8'     THEN nd.schedulers_per_node
        WHEN 'single_node_gt_8'     THEN 8
        WHEN 'multiple_nodes_le_16' THEN nd.schedulers_per_node
        ELSE CASE WHEN nd.schedulers_per_node / 2 <= 16
                  THEN nd.schedulers_per_node / 2 ELSE 16 END
        END                                         AS [numa.maxdop_guidance],
    dop.basis                                       AS [numa.maxdop_guidance_basis],
    (SELECT CONVERT(int, value_in_use) FROM sys.configurations
      WHERE name = 'max degree of parallelism')     AS [numa.maxdop_configured],

    CONVERT(bit, si.virtual_machine_type)           AS [machine.is_virtual],
    si.virtual_machine_type_desc                    AS [machine.virtual_machine_type]
FROM sys.dm_os_sys_info AS si

-- The two numbers Microsoft's table is indexed by, taken over the same rows so
-- that they cannot disagree about which nodes are real. A node with no online
-- scheduler is not a node a parallel query can run inside, so counting it would
-- drag the minimum to zero and the guidance to zero with it, and would inflate
-- the node count into the multi-node half of the table on the strength of a
-- node nothing runs on. The DAC node is excluded for the reason it is excluded
-- above: its single scheduler is not available to anything a MAXDOP applies to,
-- and left in it would make every instance recommend 1.
--
-- This node count is therefore not always numa.scheduler_node_count, which
-- counts every non-DAC node whether or not it carries a scheduler. They differ
-- exactly when a node has been emptied, and when they differ this is the one
-- the recommendation has to be built on.
CROSS APPLY (SELECT MIN(n.online_scheduler_count), COUNT(*)
               FROM sys.dm_os_nodes AS n
              WHERE n.node_state_desc NOT LIKE '%DAC%'
                AND n.online_scheduler_count > 0)   AS nd(schedulers_per_node, nodes)

CROSS APPLY (SELECT CASE
        WHEN nd.schedulers_per_node IS NULL                THEN 'logical_processors_fallback'
        WHEN nd.nodes = 1 AND nd.schedulers_per_node <= 8  THEN 'single_node_le_8'
        WHEN nd.nodes = 1                                  THEN 'single_node_gt_8'
        WHEN nd.schedulers_per_node <= 16                  THEN 'multiple_nodes_le_16'
        ELSE 'multiple_nodes_gt_16' END)            AS dop(basis)

CROSS APPLY (SELECT COUNT(*) FROM sys.dm_os_schedulers
              WHERE status LIKE 'VISIBLE%' AND is_online = 0)
                                                    AS sch(schedulers_offline)

-- The two affinity masks, which only sys.configurations carries. They are read
-- for one purpose: to keep a deliberate restriction from being reported as a
-- licensing ceiling.
CROSS APPLY (SELECT
        (SELECT CONVERT(bigint, value_in_use) FROM sys.configurations
          WHERE name = 'affinity mask'),
        (SELECT CONVERT(bigint, value_in_use) FROM sys.configurations
          WHERE name = 'affinity64 mask'))          AS aff(mask, mask64)

-- The edition's compute ceiling. EditionID rather than the edition string,
-- which is localised and reworded between releases.
CROSS APPLY (SELECT CONVERT(int, SERVERPROPERTY('EditionID')))
                                                    AS ed(edition_id)
CROSS APPLY (SELECT
        CASE WHEN ed.edition_id IN (1804890536, 1872460670, 610778273, 284895786,
                                    -2117995310, -1785266663, -1534726760,
                                    1293598313, -1592396055, -133711905)
             THEN 1 ELSE 0 END,
        CASE ed.edition_id
            WHEN  284895786  THEN 4     -- Business Intelligence
            WHEN -1785266663 THEN 4     -- Developer Standard, 2025 and later
            WHEN -1534726760 THEN 4     -- Standard
            WHEN  1293598313 THEN 4     -- Web, up to 2022
            WHEN -1592396055 THEN 1     -- Express
            WHEN  -133711905 THEN 1     -- Express with Advanced Services, to 2022
            ELSE NULL END,              -- Enterprise, Developer: the OS maximum
        CASE ed.edition_id
            WHEN  1804890536 THEN 20    -- Enterprise, Server plus CAL
            WHEN  284895786  THEN 16    -- Business Intelligence
            WHEN -1785266663 THEN 32    -- Developer Standard, 2025 and later
            WHEN -1534726760 THEN       -- Standard: 24 cores, 32 from version 17
                 CASE WHEN CONVERT(int, SERVERPROPERTY('ProductMajorVersion')) >= 17
                      THEN 32 ELSE 24 END
            WHEN  1293598313 THEN 16    -- Web, up to 2022
            WHEN -1592396055 THEN 4     -- Express
            WHEN  -133711905 THEN 4     -- Express with Advanced Services, to 2022
            ELSE NULL END)              -- Enterprise core, Developer, Evaluation
                                        AS lim(known, socket_limit, core_limit)

-- The order of the branches is the order of what they prove. Each of the first
-- two needs a finite limit for the edition, because neither reading means
-- anything against an unlimited one: the socket branch compares against the
-- socket limit directly, and the logical processor branch needs a core limit,
-- since a socket ceiling leaves cpu_count reporting the whole machine and
-- cannot produce hyperthread_ratio > cpu_count. The third needs no limit: it
-- claims nothing about licensing, only that processors are gone and affinity is
-- not the reason.
CROSS APPLY (SELECT CASE
        WHEN lim.known = 0 THEN 'unknown_edition'
        WHEN lim.socket_limit IS NOT NULL
         AND si.socket_count > lim.socket_limit     THEN 'sockets_over_limit'
        WHEN lim.core_limit IS NOT NULL
         AND si.hyperthread_ratio > si.cpu_count    THEN 'logical_processors_below_topology'
        WHEN si.scheduler_count < si.cpu_count
         AND aff.mask = 0 AND aff.mask64 = 0
         AND si.affinity_type_desc = 'AUTO'         THEN 'schedulers_offline_unexplained'
        ELSE 'none_observed' END)                   AS cap(signature)
OPTION (RECOMPILE, MAXDOP 1);

/* One row per node, with the memory node it belongs to. This is the evidence
   behind soft_numa_in_effect: several scheduler nodes carrying the same
   memory_node_id is soft-NUMA, one apiece is hardware NUMA. The DAC node is
   kept rather than filtered, because its absence would be more puzzling than
   its presence to anyone comparing this against sys.dm_os_nodes directly. */
SELECT
    n.node_id                                       AS [node_id],
    n.node_state_desc                               AS [state],
    n.memory_node_id                                AS [memory_node_id],
    n.online_scheduler_count                        AS [online_schedulers],
    n.cpu_count                                     AS [cpu_count],
    CONVERT(bigint, mn.virtual_address_space_reserved_kb / 1024)
                                                    AS [memory_reserved_mb]
FROM      sys.dm_os_nodes        AS n
LEFT JOIN sys.dm_os_memory_nodes AS mn
       ON mn.memory_node_id = n.memory_node_id
ORDER BY n.node_id
OPTION (RECOMPILE, MAXDOP 1);
