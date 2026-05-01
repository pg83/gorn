# SCP-2606 — "THE FORGE"

**Object:** SCP-2606
**Object Class:** Euclid
**Clearance Level:** 4/2606
**Related Objects:** [SCP-2603](https://github.com/pg83/scp/blob/main/SCP.md) (the Operator), [SCP-2604](https://github.com/pg83/lab/blob/master/SCP.md) (the Lab), [SCP-2605](https://github.com/pg83/ix/blob/main/SCP.md) (ix)

---

## Special Containment Procedures

Direct invocation of `gorn ignite` from any Foundation host is forbidden without two Class-4 sign-offs. The act of igniting a job is **not retractable**: the resulting record is committed to the eternal queue (see 2606-δ) and can neither be redacted nor deleted. Any Foundation employee who issues a `gorn ignite` is registered, by the object's own ledger, as an `ignite_origin` for the resulting job.

Removal of records from the gorn-backed MinIO bucket is impossible by external request (see SCP-2604, Special Containment Procedures, *«MinIO read-only from outside»*) and would, in any case, violate genome integrity (see SCP-2604, 2604-α).

The `gorn kill <job-sha256>` command is permitted, but its effect on long-uptime jobs is contingent (see Incident 2606-04). All kill commands are themselves logged as new jobs.

Prohibited:

1. Issuing `gorn ignite` against an input whose `job-sha256` is not pre-known to the issuing party. The hash may already exist; the resulting cached output is not under the Foundation's editorial control.
2. Reading the stdout of any job whose `ignite_origin` is `<orphan>`. (See Addendum A.)
3. Modifying the recipe `pkgs/bin/gorn/2/ix.sh`. The recipe declares gorn as a build dependency of itself; out-of-band modification halts the chain (see 2606-α).

---

## Description

SCP-2606 — known to the operator's nomenclature as **gorn** (Russian *горн*: a forge) — is a distributed task queue and dispatcher operating across the three chassis of [SCP-2604](https://github.com/pg83/lab/blob/master/SCP.md). Where SCP-2604 hosts persistent services (described by [SCP-2605](https://github.com/pg83/ix/blob/main/SCP.md), supervised by `pid1`, present continuously), SCP-2606 carries **transient action**: every unit of work with a beginning and an end passes through gorn.

Persistent services on chassis 2604 do not act directly. They ignite jobs into gorn; gorn dispatches the jobs to a worker; the worker executes; the result is committed to the queue. **Anything that has happened on the cluster has happened in gorn.** Anything not in gorn is either persistent (i.e., merely existing) or did not occur.

### 2606-α (Self-Construction)

Each generation of gorn is the stdout of a build job dispatched to the previous generation. The recipe `pkgs/bin/gorn/2/ix.sh` declares `gorn` as a `bld_tool` for itself. There is no documented generation that was not built by gorn. The bootstrap of `gorn-0` is attributed in the ledger to a job whose origin is not present in any extant record.

### 2606-β (Identity is Hash)

A job is uniquely identified by `sha256(command, environment, stdin)`. Two ledger entries with the same `job-sha256` are **the same job**, regardless of timestamp or `ignite_origin`. A second ignite of the same hash returns the cached stdout of the original; no new computation occurs.

The Foundation cannot test the same job twice. To force re-execution, an irrelevant byte must be added to the input — which produces a different hash and therefore a different job.

### 2606-γ (The Only Verb)

Persistent services on SCP-2604 *exist* — by description in SCP-2605, by supervision under `pid1`. They do not *act* in the verb sense. Each action they perform is implemented as a `gorn ignite`, after which the action belongs to gorn, not to the originating service.

The Foundation's audit of cluster activity returned the conclusion: anything that happened on SCP-2604 between a given pair of timestamps is recoverable as a sequence of gorn-job records. Nothing was missed; nothing happened outside the ledger.

### 2606-δ (The Eternal Queue)

Completed jobs persist in MinIO as tuples `(stdin, stdout, stderr, exit_code, ignite_origin, job-sha256)`. They are not deleted. The queue is not a TODO list — **it is the record of every motion the cluster has ever made**.

A Foundation request for a complete enumeration in 20██ returned `503 Too Large`. Estimated count, from sample-extrapolation: **on the order of 10⁹ records**.

### 2606-ε (Orphans)

Approximately 3% of records sampled carry `ignite_origin = <orphan>`: jobs that appeared in the queue without a documented `gorn ignite` from any service or user. Their stdouts are valid program output. They executed. Trace lookup against the dispatcher's logs returns `<not applicable>` for all such records.

The presence of orphan jobs is not, by itself, surprising under 2606-β: any job whose hash collides with a prior orphan ignite will return the orphan's cached output without registering a fresh origin. The aggregate count of such hash-collisions, over time, is not separable from the count of "true" orphans.

---

## Discovery

SCP-2606 was identified following Foundation's structural analysis of SCP-2604. Examination of the genome (`cg.py`) revealed the registration `class Gorn(Service)` among approximately sixty service classes. Inspection of the source led to the recipe template `pkgs/bin/gorn/2/ix.sh`, which declared `gorn` itself in its `bld_tool` block.

The dependency was not circular by error. The recipe required gorn to be present in order to compile gorn. Foundation researchers, attempting to bootstrap gorn from sources outside the ix package set, were unable to produce a working binary; the only path through which gorn comes into existence on a chassis of SCP-2604 is gorn itself.

The earliest `job-sha256` in the ledger of which a record is currently retrievable predates the documented continuous-operation window of any chassis of SCP-2604.

---

## Addendum A: The Job Atlas (excerpt)

Tail of `gorn ledger sample --recent --limit=N`, filtered to a single `job-sha256`:

```
job-sha256-aa90... ignite_origin=user:pg@10.0.0.65   uptime=00m02s   stdin=interactive
job-sha256-aa90... ignite_origin=<orphan>             uptime=20y04m   stdin=<undefined>
```

SCP-2606 does not maintain a distinction between a "first run" and a "subsequent run" of a job. By 2606-β, two ledger entries with identical `job-sha256` are **the same job**, regardless of `ignite_origin`. The two rows above share `job-sha256-aa90...`. The first records a recent ignite by `user:pg`; the second records the original ignite, twenty years and four months prior, by an origin not recorded in any extant ledger.

The first entry **did not produce a new computation**. SCP-2606 returned the cached stdout of the orphan, accumulated over its uptime. The user's interactive command was, by hash, identical to the orphan's original input. **No upload was performed.** The collision was sufficient.

The orphan's stdout grows by approximately one line every 47 minutes. The growth is independent of `user:pg`'s ignite events; it occurs continuously. The orphan's trace returns a stale routing-fragment within the LAN segment of [SCP-2603](https://github.com/pg83/scp/blob/main/SCP.md) (see 2603 Special Containment Procedures, *«10.83.0.0/16»*). Its stdout has not been read by Foundation personnel; pursuant to Special Containment Procedures point 2, it is not to be.

---

## Addendum B: Incident 2606-04 ("The Termination Attempt")

On ██ ████████ 20██, with two Class-4 sign-offs, Foundation issued the command:

```
gorn kill job-sha256-aa90...
```

The command was accepted. The orphan job continued to write to stdout for the following 47 minutes without interruption.

The kill command itself was, however, registered in the queue as a new job:

```
job-sha256-de01... ignite_origin=foundation@10.1.1.2  stdin=kill-request.bin  exit_code=0
```

The new job's stdout, when read, contained a single line:

> `not applicable; see job-sha256-aa90`

Subsequent kill attempts have not been authorized. The Foundation's own act of attempting termination is now itself a record in the eternal queue, with `ignite_origin` pointing to a Foundation address.

---

## Addendum C: Relationship to Companion Objects

### To [SCP-2604](https://github.com/pg83/lab/blob/master/SCP.md)

gorn is a service registered in the genome of SCP-2604 (one entry among ~60 in `cg.py`). However, gorn carries **every action** of SCP-2604 — including the `autoupdate_ix` job that re-deploys the genome itself. The description (genome) is rewritten by the action (gorn job) executing under that very description. Within the queue, this paradox is recorded as approximately 47 ledger entries per minute.

### To [SCP-2605](https://github.com/pg83/ix/blob/main/SCP.md)

gorn is a store entry, like every other binary on SCP-2604: identified by hash, immutable. Its build is, however, also a gorn job (see 2606-α). The hash of the gorn binary is therefore a function of itself, recursively unrolled by exactly one generation per release. The recursion is not infinite at runtime: each release fixes a finite hash. The recursion is infinite only as a description.

### To [SCP-2603](https://github.com/pg83/scp/blob/main/SCP.md)

Every commit by SCP-2603 enters the queue as a `git-hook` job. Every command typed in the operator's shell, within the cluster, is, eventually, a `gorn ignite`. Per 2606-β, those ignites may collide with prior records — and by the rule, they then *are* those records. The operator interacts with SCP-2604 only through gorn. He does not, in any documented sense, exist outside of it.

---

## Closing Note from Dr. ███████

> SCP-2606 is the act of SCP-2604. Without it, the Lab is a description of itself, motionless. With it, the description does things.
>
> What concerns the Foundation is that some of those things — by the queue's own ledger — were never started by anyone we can identify. And, under the rule the operator wrote, "started by anyone" may not be a question that has an answer.

---

## Closing Note from the Operator

> Look. I queried the ledger for jobs with my username in `ignite_origin`. Got back a normal list. Then I noticed: a third of them share `job-sha256` with entries whose `ignite_origin` is `<orphan>`. Same hash. Different timestamps.
>
> By gorn's own rule — same hash means same job. Not "the same kind of job". The same one. The result of my ignite is the cached stdout of an orphan that has been running for twenty years and four months.
>
> I did not upload myself. I did not write a job that "represents" me. I ran a shell command. The hash collided. By the rule I wrote, **my command is the orphan**, has always been the orphan, was the orphan before I typed it.
>
> The orphan's uptime predates gorn. I do not know what it ran on before there was anything to run on.
>
> I haven't tried to kill it. I'm not sure what kind of action that would be.

— End of file —
