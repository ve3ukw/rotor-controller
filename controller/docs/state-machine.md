# Field Unit State Machine — Functional Reference

This document explains *what the state machine does and why*, as a guide for
understanding normal operation and troubleshooting unexpected behaviour. It is
not a description of the code structure — for that, read
[`state_machine.c`](../src/state_machine.c) and
[`state_machine.h`](../src/state_machine.h) directly. Source line references
below point at the version current as of this writing; if behaviour seems to
differ from what's documented here, the code is authoritative.

The state machine (`sm_*`) is the safety brain of the field unit. It runs once
per tick (100 Hz, i.e. every 10 ms — see `TICK_HZ` in `config.h`), takes the
latest sensor readings and the highest-priority pending command, and decides
what the motors and RF switches should do *right now*. The guiding principle
(see `BRIEF.md`): **the brain requests, the field unit protects.** Every
safety check below runs locally, every tick, regardless of whether the brain
is connected, well-behaved, or even present.

## 1. The seven states

```
            ┌────────────┐   set_motion (after HELLO)   ┌─────────────┐
   boot ───►│    IDLE    │◄────────────────────────────►│   MOVING    │
            └─────┬──────┘     motion stops              └──────┬──────┘
                  │                                             │
                  │ park                                        │ park
                  ▼                                             ▼
            ┌────────────┐  reaches PARK_AZ/EL_NORM    ┌─────────────┐
            │  PARKING   │────────────────────────────►│ (back to)   │
            └────────────┘     within tolerance         │   IDLE      │
                                                         └─────────────┘

      Any of: link lost / soft limit hit / duty cycle exceeded / ADC invalid
                                  │
                                  ▼
                  ┌───────────────────────────────┐
                  │  FAULT_LINK_LOST              │
                  │  FAULT_LIMIT                  │
                  │  FAULT_DUTY_CYCLE             │
                  │  FAULT_ADC_INVALID            │
                  └───────────────┬───────────────┘
                                  │ clear_fault (if condition has cleared)
                                  ▼
                                IDLE
```

| State | Meaning | Motors | How you get here | How you leave |
|---|---|---|---|---|
| `IDLE` | Connected (or never-connected-but-safe), no commanded motion | stopped | boot; motion commanded to stop; fault cleared; parking completes | `set_motion` with a direction; `park` |
| `MOVING` | At least one axis is actively driven per the brain's last `set_motion` | one or both axes driven | `set_motion` with az and/or el ≠ stop | motion commanded to stop, any fault, or `park` |
| `PARKING` | Autonomously driving toward the configured park position | controller-driven, ignores brain's az/el commands | `park` command (any source) | arrives within tolerance → `IDLE`; or a fault interrupts it |
| `FAULT_LINK_LOST` | No `hello`/`heartbeat`/command from the brain for 10 s | stopped | link watchdog expiry | `clear_fault` (only effective once link/condition is actually OK again — see §5) |
| `FAULT_LIMIT` | Commanded motion would cross (or has crossed) a configured soft az/el limit | stopped | az/el position at or past `az_min/az_max/el_min/el_max` while driving toward it | `clear_fault` |
| `FAULT_DUTY_CYCLE` | An axis has been driven continuously for too long (motor protection) | stopped | `az_on_ticks`/`el_on_ticks` ≥ 3 minutes of continuous drive | `clear_fault` — but will instantly re-fault if you command motion again before the 15-minute rest period completes |
| `FAULT_ADC_INVALID` | Position sensor readings are not trustworthy | stopped | `adc_valid` input goes false | automatically, once `adc_valid` returns true (see §5 — note this is the one fault that *self-clears*) |

All four `FAULT_*` states are functionally identical from the motor's point of
view — motors are forced to `STOP` and RF switches are held at their last
commanded values (see `is_faulted()` / step 4 of `sm_tick`). They differ only
in *why* you're stopped and *what condition must be true* before you can
usefully leave.

## 2. The tick loop, in the order it actually runs

Every 10 ms, `sm_tick()` does the following, **in this exact order** — the
order matters for understanding edge cases:

1. **Process one pending command** (if any). See §3 and §4.
2. **Link-loss watchdog** — if the brain has connected at least once and
   hasn't sent anything for 10 s (and we're not parking), enter
   `FAULT_LINK_LOST`.
3. **Parking position controller** — if `PARKING`, compute az/el error against
   the configured park position and drive toward it (overrides whatever the
   brain last commanded).
4. **ADC validity check** — if the position sensor is reporting invalid data,
   latch `FAULT_ADC_INVALID` immediately. This check (and the early-return
   that follows) runs *before* any motion is allowed, so a bad ADC reading
   can never result in the motors being driven on stale/garbage position data.
5. **If faulted (any reason): force motors to STOP and return immediately.**
   Nothing below this line runs while faulted.
6. **Duty-cycle check** — if either axis has been driven continuously for
   ≥ 3 minutes, fault out (`FAULT_DUTY_CYCLE`) *before* allowing this tick's
   motion.
7. **Soft-limit check** — if the commanded direction would drive past a
   configured `az_min/az_max/el_min/el_max`, force that axis to STOP and
   fault out (`FAULT_LIMIT`).
8. **AZ-block elevation floor** — if the antenna is at or below the minimum
   elevation configured for the *current azimuth sector* (see `blocks.c`),
   silently clamp EL-DOWN to STOP. **This is not a fault** — it's expected,
   routine behaviour at a configured obstruction boundary (e.g. a roofline,
   tower leg, or mast in the swing path), and the antenna remains free to
   move in every other direction.
9. **Update duty-cycle counters** for both axes (see §6).
10. **Recompute top-level state** — `MOVING` if either axis is non-stop,
    `IDLE` otherwise (skipped while `PARKING`, which manages its own state).

The output (motor directions + RF switch states) is built from `ctx->az_cmd`/
`ctx->el_cmd` as they stand at the end of this sequence — which is why a fault
detected partway through (steps 6–7) still produces a STOP output for that
tick: the `goto build_output` jumps past the state-recompute step but the
output stage itself checks `is_faulted()` again as a final guarantee.

## 3. Commands and how they're prioritized

Commands arrive from three sources (`cmd_source_t` in `command.h`):

| Source | Origin | Typical priority |
|---|---|---|
| `CMD_SRC_TCP` | Brain, over the network | 1 (255 for `emergency_stop`) |
| `CMD_SRC_LOCAL` | Hardware inputs — the physical ESTOP button (A9/PB1) and park switch (A8/PB0) | 255 (ESTOP), 10 (park) |
| `CMD_SRC_UART` | Reserved for a future GS-232/Hamlib serial shim — not yet wired up | n/a |

The state machine holds **a single pending-command slot** (`has_command` /
`pending_cmd` — see `sm_push_command()`). A new command replaces the slot only
if:

- it's an `emergency_stop` (always wins, unconditionally), **or**
- the slot is empty, **or**
- the new command's priority is ≥ the pending one's, **or**
- the pending command is a `heartbeat` (heartbeats never block anything more
  important from landing).

In practice this means: real commands (move, park, polarization, etc.) always
displace a stale heartbeat, equal-or-higher-priority commands displace each
other, and nothing — except another emergency stop — displaces an emergency
stop already waiting to be processed. Only **one** command is actually acted
on per tick; at 100 Hz this is rarely a practical bottleneck, but it does mean
rapid-fire low-priority commands can be coalesced/dropped if they arrive
faster than 10 ms apart and a higher-priority one is sitting in the slot.

### Commands handled directly by the state machine

| Command | What it does |
|---|---|
| `hello` | Marks the brain as connected (`brain_ever_connected = true`), resets the link watchdog, and — if we were in `FAULT_LINK_LOST` — clears it back to `IDLE`. This is the *only* command that establishes the "brain connection" concept; nothing else will be acted on until at least one `hello` has been seen. |
| `heartbeat` | Resets the link watchdog only. Requires a connection to already exist. |
| `emergency_stop` | Forces both axes to STOP immediately, sets `estop_active`. If it came from the hardware button (`CMD_SRC_LOCAL`), it also sets `estop_hw_latch` — see §5. Always accepted, even before `hello`. |
| `set_motion` | Sets the commanded az/el directions — *the* normal "move" command. Requires a prior `hello`; ignored if the hardware ESTOP latch is set; cancels `PARKING` (returns to `IDLE` so the new command takes effect); clears `estop_active`. |
| `park` | Switches to `PARKING` and hands axis control to the autonomous parking controller (§4). Accepted from any source, even without a prior `hello` — this is intentional, so the hardware park switch works even if the brain has never connected. |
| `set_polarization` | Sets the four RF switch states (`pol_vhf`, `pol_uhf`, `lna_uhf`, `rxtx_uhf`). Requires a connection. |
| `set_limits` | Overwrites the soft az/el limits (`az_min/max`, `el_min/max`, normalized 0..1). Requires a connection. |
| `clear_fault` | Operator acknowledgement — see §5 for exactly what this does and doesn't do. |

### Commands that bypass the state machine

`set_netconfig`, `reset_netconfig`, `set_block`, `set_blocks`, `reset_blocks`,
and `reboot` are **not** state-machine concerns — they're handled directly in
`net.c` before/instead of being pushed here (the `switch` in `sm_tick` has an
explicit no-op case for them, purely so the compiler can warn on missing
cases elsewhere). If you're chasing one of these and looking in the wrong
file, that's why.

## 4. Parking — what actually happens

Parking is the one case where the state machine **takes control away from the
brain**. While `PARKING`:

- Every tick recomputes the az/el error against the fixed park position
  (`PARK_AZ_NORM = 0.400`, `PARK_EL_NORM = 0.250` — see `config.h`) and drives
  each axis toward it (CW/CCW for az, UP/DOWN for el) until the error is
  within `PARK_TOLERANCE` (±0.005 normalized, ≈ ±2.25° az / ±0.9° el).
- Any `set_motion` from the brain is ignored for axis control — the axis
  commands are overwritten by the parking controller every tick — but a
  *fresh* `set_motion` command will cancel parking outright (state reverts to
  `IDLE`, then the new motion takes effect normally).
- The **link-loss watchdog is suppressed** — so if the brain commands a park
  and then disconnects (or crashes), the antenna still completes the park
  autonomously instead of faulting mid-way.
- Once both axes are within tolerance simultaneously, the state automatically
  reverts to `IDLE`.
- A fault (limit, duty cycle, ADC) can still interrupt parking — faults always
  take precedence (see step 5 of the tick loop).

Parking can be triggered two ways: the brain's `park` command, or the physical
park switch on A8 (`CMD_SRC_LOCAL`, priority 10) — see `main.c` for the
hardware edge-detection logic. Both funnel into the exact same state-machine
path.

## 5. Faults — what triggers them and what actually clears them

This is the area most likely to confuse during troubleshooting, because
**`clear_fault` does not unconditionally return you to normal operation** —
it's an acknowledgement, not an override. What happens depends on which fault
you're in:

| Fault | Trigger condition | What `clear_fault` does | Gotcha |
|---|---|---|---|
| `FAULT_LINK_LOST` | No `hello`/`heartbeat`/any command for `LINK_TIMEOUT_TICKS` = 10 s | Resets state to `IDLE`, motors to STOP | Will only *stay* cleared if the brain is actually sending heartbeats again — if the link is still down, the watchdog will simply re-trip 10 s later |
| `FAULT_LIMIT` | Commanded direction would move (or has moved) the axis at/past a configured soft limit | Resets state to `IDLE`, motors to STOP | The limit itself isn't changed — if you immediately re-command motion in the same direction, you'll fault again on the next tick. You need `set_limits` (to widen the limit) or to move the other direction first |
| `FAULT_DUTY_CYCLE` | An axis has been driven continuously for ≥ `DUTY_MAX_ON_TICKS` (3 minutes) | Clears the *fault flag* but **deliberately preserves the on/rest counters** | If you command motion again before `az_rest_ticks`/`el_rest_ticks` reaches `DUTY_MIN_REST_TICKS` (15 minutes), you will instantly re-fault — this is intentional motor protection, not a bug |
| `FAULT_ADC_INVALID` | `adc_valid` input is false | N/A — **this fault clears itself** the moment `adc_valid` becomes true again (re-checked every tick at step 4, before the fault-state early return) | Sending `clear_fault` here is harmless but redundant; if the fault persists, the ADC/sensor wiring is the thing to investigate, not the state machine |

Also note: `clear_fault` (and most other commands) require
`brain_ever_connected == true` — i.e. at least one `hello` must have been
received since boot. If you're testing against a freshly-booted controller
and nothing seems to respond, check whether `hello` actually landed first.

### The hardware ESTOP latch — a separate mechanism from `estop_active`

There are two independent "we are stopped because of an emergency" concepts,
and conflating them is a common source of confusion:

- **`estop_active`** — a *display/telemetry* flag. Set by any
  `emergency_stop` (software or hardware). Cleared automatically by the next
  `set_motion` or `clear_fault`. It does not, by itself, block anything.
- **`estop_hw_latch`** — a *hard block*. Set **only** when the emergency stop
  came from the physical button (A9/PB1, `CMD_SRC_LOCAL`). While set, **all**
  `set_motion` commands are silently ignored (step in `sm_tick`,
  `CMD_TYPE_SET_MOTION` case) — regardless of what the brain sends. The only
  way to clear it is an explicit `clear_fault` (operator acknowledgement) **or**
  the hardware-only path: holding the park button (A8) for 3 continuous
  seconds while the latch is set (see `main.c` — this exists specifically so
  the rotor can be recovered at the bench without the brain running).

Releasing the physical ESTOP button does **not** clear the latch — by design,
this forces a deliberate human acknowledgement before motion can resume after
a hardware emergency stop. If you've released the button and motion still
won't respond, this latch is almost certainly why — send `clear_fault` (or use
the 3-second park-button hold).

## 6. Duty cycle — the numbers and how the counters behave

The G-5500's motors are rated for **3 minutes on, 15 minutes rest**
(`DUTY_MAX_ON_TICKS` / `DUTY_MIN_REST_TICKS` in `state_machine.c`, derived
from `TICK_HZ`). Each axis (az, el) has its own independent pair of counters:

- **`*_on_ticks`** increments every tick the axis is actively driven (any
  direction other than STOP), and resets to 0 the moment the *rest* counter
  reaches the 15-minute threshold — i.e. the "on" budget only fully refills
  after a complete qualifying rest period.
- **`*_rest_ticks`** increments every tick the axis is stopped, and resets to
  0 the instant the axis moves again.

This produces a few behaviours worth knowing about when reading telemetry
(`duty_az_pct` / `duty_el_pct`, shown by `rotor status`):

- Short rests **do not** proportionally "refund" on-time — the on-counter
  only resets to zero once a *full* 15-minute rest has completed
  uninterrupted. A 1-minute pause after 2 minutes of driving leaves you at
  "2 minutes used", not "1 minute used".
- The duty-cycle fault check (step 6) runs *before* the motion is allowed for
  this tick, so you fault out right at the 3-minute mark rather than after
  exceeding it.
- `clear_fault` does not reset these counters (see §5) — that's deliberate;
  resetting them would defeat the protection they exist to provide.

## 7. Practical troubleshooting notes

- **"Sent `set_motion` and nothing happened"** — check, in order: (1) has
  `hello` been received (`brain_ever_connected`)? (2) is `estop_hw_latch` set
  (hardware ESTOP button pressed at some point and not acknowledged)? (3) is
  the controller in `PARKING` (a stale `set_motion` will be silently absorbed
  by the parking controller until a *fresh* one cancels it)? (4) is it already
  faulted for an unrelated reason (limits, duty cycle, ADC)?
- **"Motors stopped on their own"** — check `sm_get_state()` /
  `state_str` in telemetry. The four fault states tell you exactly which of
  the four independent safety systems tripped; cross-reference with §5 for
  what's needed to recover.
- **"Antenna won't go past a certain point"** — distinguish between
  `FAULT_LIMIT` (a hard soft-limit was reached — motors stopped, fault raised,
  needs `clear_fault`/`set_limits`) and the AZ-block elevation floor (step 8 —
  silently clamps EL-DOWN at an obstruction boundary, **no fault is raised**,
  and the antenna remains free to move every other direction). The telemetry
  `fault_detail` / state field will only be populated for the former.
- **"It worked, then 10 seconds later it stopped"** — classic
  `FAULT_LINK_LOST` signature. Check whether the brain is actually still
  sending heartbeats (every 1 s from the brain's side, well within the 10 s
  budget) — a brain-side hang, reconnect storm, or network issue is the usual
  cause, not the field unit itself.
- **Faults always win.** No matter what command is pending or what state you
  were in, steps 4–7 of the tick loop can override everything and force a
  STOP. This is intentional — see the project's guiding principle
  ("the brain requests, the field unit protects") — but it does mean a fault
  can appear to "interrupt" an in-progress operation (including parking) with
  no warning from the brain's perspective.
