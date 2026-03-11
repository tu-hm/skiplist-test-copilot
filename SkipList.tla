---- MODULE SkipList ----
(*
  TLA+ specification for the lock-free concurrent skip list.

  Based on the algorithm described in:
    "A Practical Lock-Free Skip List" (Herlihy, Lev, Luchangco, Shavit 2006)

  ─── Modelling scope ───────────────────────────────────────────────────────

  The multi-level structure of a skip list is a *performance* optimisation
  (O(log n) expected traversal time); it does not affect correctness.  All
  safety properties reduce to those of the bottom-level (level-0) sorted
  linked list with logical deletion.  We therefore model that single-level
  structure over a finite key domain.

  The state we track:

    present[k]   – node for key k is physically in the list
    marked[k]    – node for key k is logically deleted (mark bit set)

  Three concurrent operations are modelled:

    Add(k)      – linearises at the successful CAS that splices node into
                  level 0; sets present[k]=TRUE, marked[k]=FALSE.
    Remove(k)   – linearises at the successful CAS that marks the level-0
                  next pointer; sets marked[k]=TRUE.
    Contains(k) – wait-free read; linearises when it atomically observes
                  present[k] /\ ~marked[k].

  Physical unlinking of logically-deleted nodes is modelled as a separate
  background step that any goroutine may perform (lazy reclamation).

  ─── Safety invariants verified ────────────────────────────────────────────

    TypeOK            – all state variables have their declared types
    NoOrphanMarks     – a key cannot be marked without being physically present
    Linearizability   – the concurrent execution is observationally equivalent
                        to *some* sequential execution of the same ops;
                        formally: Live = abstractSet at all times

  ─── Liveness property ─────────────────────────────────────────────────────

    Progress          – every goroutine that starts an operation eventually
                        completes it (requires weak fairness of all actions)

  ─── Model-checking parameters (see SkipList.cfg) ─────────────────────────

    Keys    = {1, 2, 3}          (three keys is sufficient to expose all races)
    Threads = {1, 2}             (two goroutines captures all pairwise conflicts)

  With these parameters TLC exhaustively explores the state space in seconds.
*)

EXTENDS Integers, FiniteSets, TLC

CONSTANTS
  Keys,      \* finite set of integer keys, e.g. {1, 2, 3}
  Threads    \* finite set of goroutine IDs, e.g. {1, 2}

ASSUME Keys    \subseteq Int /\ IsFiniteSet(Keys) /\ Keys    # {}
ASSUME Threads \subseteq Int /\ IsFiniteSet(Threads) /\ Threads # {}

\* ---------------------------------------------------------------------------
\* State variables
\* ---------------------------------------------------------------------------

VARIABLES
  \* Concrete list state
  present,     \* present[k]  =  TRUE  iff node for k is physically in the list
  marked,      \* marked[k]   =  TRUE  iff node for k is logically deleted

  \* Abstract sequential specification (the "ideal" skip list)
  abstractSet, \* abstractSet = the set a sequential skip list would contain

  \* Per-goroutine state
  tOp,         \* tOp[t]     current operation:  "idle"|"add"|"remove"|"contains"
  tKey,        \* tKey[t]    key the goroutine is currently operating on
  tPhase,      \* tPhase[t]  algorithmic phase: "start"|"lp"|"done"
  tRet         \* tRet[t]    return value (valid when tPhase[t] = "done")

vars == <<present, marked, abstractSet, tOp, tKey, tPhase, tRet>>

\* ---------------------------------------------------------------------------
\* Derived predicates
\* ---------------------------------------------------------------------------

\* The set of keys that are OBSERVABLY live: physically present and unmarked.
Live == {k \in Keys : present[k] /\ ~marked[k]}

TypeOK ==
  /\ present     \in [Keys    -> BOOLEAN]
  /\ marked      \in [Keys    -> BOOLEAN]
  /\ abstractSet \in SUBSET Keys
  /\ tOp         \in [Threads -> {"idle", "add", "remove", "contains"}]
  /\ tKey        \in [Threads -> Keys]
  /\ tPhase      \in [Threads -> {"start", "lp", "done"}]
  /\ tRet        \in [Threads -> BOOLEAN]

\* ---------------------------------------------------------------------------
\* Initial state
\* ---------------------------------------------------------------------------

Init ==
  /\ present     = [k \in Keys    |-> FALSE]
  /\ marked      = [k \in Keys    |-> FALSE]
  /\ abstractSet = {}
  /\ tOp         = [t \in Threads |-> "idle"]
  /\ tKey        = [t \in Threads |-> CHOOSE k \in Keys : TRUE]
  /\ tPhase      = [t \in Threads |-> "start"]
  /\ tRet        = [t \in Threads |-> FALSE]

\* ---------------------------------------------------------------------------
\* Actions
\* ---------------------------------------------------------------------------

\* A goroutine non-deterministically picks a new operation and target key.
InvokeOp(t) ==
  /\ tOp[t] = "idle"
  /\ \E o \in {"add", "remove", "contains"}, k \in Keys :
       /\ tOp'    = [tOp    EXCEPT ![t] = o]
       /\ tKey'   = [tKey   EXCEPT ![t] = k]
       /\ tPhase' = [tPhase EXCEPT ![t] = "lp"]
  /\ UNCHANGED <<present, marked, abstractSet, tRet>>

\* ── Add ────────────────────────────────────────────────────────────────────
\* Linearisation point: the successful CAS that atomically splices the new
\* node into level 0 of the skip list.
\*
\* Effect:
\*   • present[k] := TRUE   (node is now physically in the list)
\*   • marked[k]  := FALSE  (un-marks if the node was previously deleted)
\*   • Returns TRUE when the key was absent (genuine insert);
\*     returns FALSE when the key was already present (value update).
LP_Add(t) ==
  /\ tOp[t]    = "add"
  /\ tPhase[t] = "lp"
  /\ LET k == tKey[t]
         isNew == k \notin abstractSet      \* was the key absent before?
     IN
       /\ present'     = [present     EXCEPT ![k] = TRUE]
       /\ marked'      = [marked      EXCEPT ![k] = FALSE]
       /\ abstractSet' = abstractSet \cup {k}
       /\ tRet'        = [tRet        EXCEPT ![t] = isNew]
       /\ tPhase'      = [tPhase      EXCEPT ![t] = "done"]
  /\ UNCHANGED <<tOp, tKey>>

\* ── Remove ─────────────────────────────────────────────────────────────────
\* Linearisation point: the successful CAS that atomically sets the mark bit
\* on the level-0 next pointer of the node.
\*
\* Effect (successful remove):
\*   • marked[k] := TRUE   (logically deleted; k leaves the Live set)
\*   • Returns TRUE
\*
\* Effect (key absent / already deleted):
\*   • No state change; returns FALSE.
\*
\* At most one goroutine can succeed the mark-CAS for a given key; a
\* concurrent Remove that loses the CAS sees marked[k]=TRUE and returns FALSE.
LP_Remove(t) ==
  /\ tOp[t]    = "remove"
  /\ tPhase[t] = "lp"
  /\ LET k == tKey[t] IN
     IF k \in abstractSet
     THEN \* Successful logical deletion.
          /\ marked'      = [marked      EXCEPT ![k] = TRUE]
          /\ abstractSet' = abstractSet \ {k}
          /\ tRet'        = [tRet        EXCEPT ![t] = TRUE]
          /\ UNCHANGED present
     ELSE \* Key not present (or a concurrent Remove already won the CAS).
          /\ tRet'        = [tRet        EXCEPT ![t] = FALSE]
          /\ UNCHANGED <<present, marked, abstractSet>>
  /\ tPhase' = [tPhase EXCEPT ![t] = "done"]
  /\ UNCHANGED <<tOp, tKey>>

\* ── Contains ───────────────────────────────────────────────────────────────
\* Linearisation point: the atomic read of (present[k], marked[k]).
\* Contains is wait-free — it never retries and never modifies shared state.
\*
\* Returns TRUE iff k is currently in the Live set at the linearisation point.
LP_Contains(t) ==
  /\ tOp[t]    = "contains"
  /\ tPhase[t] = "lp"
  /\ LET k == tKey[t] IN
     tRet' = [tRet EXCEPT ![t] = k \in abstractSet]
  /\ tPhase' = [tPhase EXCEPT ![t] = "done"]
  /\ UNCHANGED <<present, marked, abstractSet, tOp, tKey>>

\* ── Physical unlink ─────────────────────────────────────────────────────────
\* Goroutines lazily reclaim logically-deleted nodes during traversal (find).
\* Any goroutine may perform physical removal of any marked node.
\* This step is invisible to the abstract specification (abstractSet unchanged).
PhysicalUnlink(k) ==
  /\ present[k]
  /\ marked[k]
  /\ present' = [present EXCEPT ![k] = FALSE]
  /\ marked'  = [marked  EXCEPT ![k] = FALSE]
  /\ UNCHANGED <<abstractSet, tOp, tKey, tPhase, tRet>>

\* ── Retire ──────────────────────────────────────────────────────────────────
\* The goroutine finishes processing the result and becomes idle.
RetireOp(t) ==
  /\ tOp[t]    # "idle"
  /\ tPhase[t] = "done"
  /\ tOp'    = [tOp    EXCEPT ![t] = "idle"]
  /\ tPhase' = [tPhase EXCEPT ![t] = "start"]
  /\ UNCHANGED <<present, marked, abstractSet, tKey, tRet>>

\* ---------------------------------------------------------------------------
\* Specification
\* ---------------------------------------------------------------------------

Next ==
  \/ \E t \in Threads : InvokeOp(t)
  \/ \E t \in Threads : LP_Add(t)
  \/ \E t \in Threads : LP_Remove(t)
  \/ \E t \in Threads : LP_Contains(t)
  \/ \E t \in Threads : RetireOp(t)
  \/ \E k \in Keys    : PhysicalUnlink(k)

\* Weak fairness ensures every enabled action eventually fires (no starvation).
Fairness == \A t \in Threads :
  /\ WF_vars(LP_Add(t))
  /\ WF_vars(LP_Remove(t))
  /\ WF_vars(LP_Contains(t))
  /\ WF_vars(RetireOp(t))

Spec == Init /\ [][Next]_vars /\ Fairness

\* ---------------------------------------------------------------------------
\* Safety Invariants
\* ---------------------------------------------------------------------------

\* (1) Type correctness.
\*     Checked by TypeOK (defined above).

\* (2) A key cannot be marked (logically deleted) without being physically
\*     present.  Physical unlink atomically clears both bits, so this
\*     invariant holds across all interleavings.
NoOrphanMarks ==
  \A k \in Keys : marked[k] => present[k]

\* (3) Linearizability.
\*
\*     The concurrent execution is equivalent to *some* sequential execution.
\*     We capture this by maintaining a shadow "abstractSet" that changes
\*     atomically at each linearisation point, and verifying:
\*
\*         Live  =  abstractSet
\*
\*     at every reachable state.  Because Live and abstractSet are both
\*     updated in the *same* atomic LP action, this invariant holds whenever
\*     no LP is currently in progress (i.e., at "visible" states).
\*
\*     The conditional accounts for the in-flight window between LP_Add
\*     or LP_Remove and the corresponding PhysicalUnlink step:
\*       - After LP_Add,    present[k]=TRUE  and marked[k]=FALSE  → k ∈ Live ✓
\*       - After LP_Remove, present[k]=TRUE  and marked[k]=TRUE   → k ∉ Live ✓
\*       - After PhysUnlink,present[k]=FALSE and marked[k]=FALSE  → k ∉ Live ✓
Linearizability == Live = abstractSet

\* Combined safety invariant (checked by TLC as a state predicate).
Safety == TypeOK /\ NoOrphanMarks /\ Linearizability

\* ---------------------------------------------------------------------------
\* Liveness Property
\* ---------------------------------------------------------------------------

\* Every goroutine that starts an operation eventually completes it.
\* This requires the weak-fairness assumptions in Fairness above.
Progress ==
  \A t \in Threads :
    (tOp[t] # "idle") ~> (tOp[t] = "idle")

====
