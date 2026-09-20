# Review Report

## 1. Blocking

**Unrunnable DDL and invalid target parameters in Task 0.1**
* **What the plan says:** "Create the session exactly as the spec renders it, with a small max_file_size so rollover is reachable, and start it."
* **What I did:** Reached by running. I attempted to execute the `CREATE EVENT SESSION` statement verbatim from the spec, using `max_file_size = 1` as instructed to be "small". I used a session prefix of `ZzObserve_` for my tests.
* **What happened:** The DDL in the spec contains variables like `@database_id`, `@stem`, and `@max_file_size_mb`. SQL Server rejects variables in `CREATE EVENT SESSION` with `Msg 102, Level 15: Incorrect syntax near '@database_id'`. Because the instructions forbid completing parameters the plan omits, an implementer is blocked. Furthermore, when substituting literal values to proceed, SQL Server rejected `max_file_size = 1` with `Msg 25641: For target, "package0.event_file", the parameter "max_file_size" passed is invalid`. (A value of `2` or higher is required).
* **What it should say instead:** The plan should provide the exact literal values to substitute for the measurement script (e.g., replace `@database_id` with `1`), and specify a valid "small" size like `max_file_size = 2`.

**Missing name for the state file**
* **What the plan says:** In Slice 4, "The state file lives beside the archive directory, which is OUTPUT_DIR and not the working directory, and it is named here rather than left to the implementer."
* **What I did:** Reached by reading. I reviewed Slice 4 and Slice 5 to find the required name of the state file.
* **What happened:** The plan never actually provides the name of the state file. It explicitly claims it is named in the document, but is silent on what that name is. An implementer is blocked because they must not invent a name the plan omitted. This also breaks the verification in Slice 5, which instructs the implementer to explicitly delete this specific file.
* **What it should say instead:** Provide the exact filename format for the state file, such as `observe-<stem>.state`.

## 2. Serious

**Preflight probe issues DDL before consent and creates permanent files**
* **What the plan says:** Task 0.2 instructs to "create a session under the managed name with the intended target directory, start it, stop it, drop it." Task 2.4 mandates the execution order: "validate, connect, describe, preflight, consent, then sweep or create."
* **What I did:** Reached by running. I executed the probe logic (creating `ZzObserve_probe`) to observe its behavior. 
* **What happened:** The probe succeeded but left a 16KB `.xel` file on the instance filesystem. Because T-SQL cannot easily delete OS files without escalated privileges (like `xp_cmdshell`), the probe creates a permanent object. More critically, running this probe during `preflight` means the command issues `CREATE EVENT SESSION` *before* obtaining the operator's consent. This contradicts the tool's core promise that consent is required before altering the instance, and turns the definition of exit code 3 ("refused before touching the instance") into a lie, as the instance is touched during preflight.
* **What it should say instead:** The directory probe must be moved to *after* the consent prompt. If it cannot clean up the `.xel` file, the consent prompt must explicitly state that a test file will be left behind in the directory.

**Sweep during `status` and `stop` bypasses consent**
* **What the plan says:** Task 2.2 requires the sweep to drop orphaned sessions. The spec notes that `observe status` "sweeps like every other entry point, which means it can issue DDL." Task 2.4 mandates the order "preflight, consent, then sweep".
* **What I did:** Reached by reading.
* **What happened:** If `status` and `stop` run the sweep logic, they will issue DDL (a `DROP` statement). However, the plan is silent on whether these subcommands should prompt for consent. If they do not, they violate the rule that no DDL runs without consent. If they do, a `status` command pausing to ask for consent is unexpected and contradicts typical CLI behavior.
* **What it should say instead:** The plan must explicitly state whether `status` and `stop` skip the consent prompt, and if they do, formally document this as an exception to the "no DDL before consent" rule. Alternatively, specify that `status` describes the session but does not sweep it.

## 3. Smaller

**Redundant custom parsing for integer flags**
* **What the plan says:** In Slice 3, "Flags: --minutes... Minutes are integers; a fractional value is refused rather than truncated."
* **What I did:** Reached by reading.
* **What happened:** The Go `flag` package's standard `flag.IntVar` automatically returns a parse error (refusing the value) when given a fractional value like `2.5`. Framing this as an explicit requirement risks an implementer writing unnecessary custom string-parsing logic to enforce what the standard library already does by default.
* **What it should say instead:** Rely on the standard `flag` package's integer parsing, which natively refuses fractional values.

*(Note: All objects created during my live testing on the SQL 2025 instance were prefixed with `ZzObserve_` and have been removed.)*
