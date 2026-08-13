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

-- @min_version: 13.0.5026

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

SELECT
    si.cpu_count                                    AS [processors.logical],
    si.socket_count                                 AS [processors.sockets],
    si.cores_per_socket                             AS [processors.cores_per_socket],
    si.hyperthread_ratio                            AS [processors.hyperthread_ratio],
    si.scheduler_count                              AS [processors.schedulers],

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
    -- compare against what is configured. Eight, or the logical processor
    -- count when that is smaller.
    CASE WHEN si.cpu_count <= 8 THEN si.cpu_count ELSE 8 END
                                                    AS [numa.maxdop_guidance],
    (SELECT CONVERT(int, value_in_use) FROM sys.configurations
      WHERE name = 'max degree of parallelism')     AS [numa.maxdop_configured],

    CONVERT(bit, si.virtual_machine_type)           AS [machine.is_virtual],
    si.virtual_machine_type_desc                    AS [machine.virtual_machine_type]
FROM sys.dm_os_sys_info AS si
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
