# Adversarial Review of `observe-spec.md`

I created the following test objects on `sql2025` and dropped them after measurement:
- Sessions: `ZzObserve_Test`, `ZzObserve_Test2`, `ZzObserve_Size1`, `ZzObserve_Size2`, `ZzObserve_Size0`, `ZzObserve_Watch`, `ZzObserve_Latency`.
- Logins: `ZzObserve_Login`.
- Files: `/var/opt/mssql/log/ZzObserve*.xel`.

## 1. Blocking

**What the document says:**  
"`observe status` says whether a session is running, since when, how many events it has recorded, how many it has dropped, and how much time is left before its deadline."

**What I did (reached by running):**  
I created a session with a file target (`ZzObserve_Size2`), pushed thousands of events into it, and queried `sys.dm_xe_sessions` and `sys.dm_xe_session_targets` to see how a status command could retrieve the event count.

**What happened:**  
There is no event count in the DMVs for a file target. `sys.dm_xe_sessions` tracks `dropped_event_count` and `total_bytes_generated`, but not the captured events. The `target_data` XML in `sys.dm_xe_session_targets` only reports `<Buffers logged="X" dropped="Y"/>`. The only way to get the exact event count is to scan the `.xel` file using `sys.fn_xe_file_target_read_file`. Doing this for a `status` command forces an unbounded, expensive I/O operation (scanning potentially gigabytes of disk), and it will be inaccurate because it misses events still buffered by `MAX_DISPATCH_LATENCY`. The assumption that the event count is readily available was true for the old in-memory histogram design (which exposed bucket counts) but is false for the file target redesign.

**What it should say instead:**  
`observe status` says whether a session is running, since when, how many buffers it has logged, how many events it has dropped, and how much time is left before its deadline.

## 2. Serious

**What the document says:**  
"Where the file goes... `observe` proposes the directory holding the error log, read from `SERVERPROPERTY('ErrorLogFileName')`, which is writable by that account by definition..." (The document claims this function yields a directory).

**What I did (reached by running):**  
I connected via `sqlcmd` and ran `SELECT SERVERPROPERTY('ErrorLogFileName');`.

**What happened:**  
The function returns the absolute path to the error log *file*, not its directory (e.g., `/var/opt/mssql/log/errorlog`). If an implementation blindly uses this output as a directory to construct the capture file path (e.g., `/var/opt/mssql/log/errorlog/observe.xel`), it will fail because `errorlog` is a file.

**What it should say instead:**  
`observe` proposes the directory holding the error log, constructed by reading `SERVERPROPERTY('ErrorLogFileName')` and stripping the trailing file name. That directory is writable by that account by definition...

## 3. Smaller

**What the document says:**  
In the `MAX_DISPATCH_LATENCY` section: "Under a session declared with a 30 second latency, a read of the histogram's target_data saw events one second after they fired... The latency governs buffering, not what a reader can see." 

**What I did (reached by running):**  
I created a session (`ZzObserve_Latency`) with an `event_file` target and `MAX_DISPATCH_LATENCY=30 SECONDS`. I fired a dummy event and immediately read the file using `sys.fn_xe_file_target_read_file('ZzObserve_Latency*.xel', NULL, NULL, NULL)`.

**What happened:**  
The file reader returned 0 events. The event was entirely invisible to the reader until I explicitly stopped the session to flush the buffer. The claim that "latency governs buffering, not what a reader can see" was measured and true for the old *histogram* target, but it contradicts how the *file* target behaves. For a file target, latency strictly governs when events are flushed to disk, and thus when a reader can see them.

**What it should say instead:**  
The latency governs buffering, which means it dictates exactly when a reader can see the events in the file. It is still set low here, because with a file target it governs how much sits in memory rather than on disk when the process dies.
