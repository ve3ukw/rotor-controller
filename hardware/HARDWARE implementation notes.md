# Hardware

This document covers the analog and discrete circuits between the
TM4C123 LaunchPad (via the GPIO Breakout BoosterPack) and the outside
world — the Yaesu G-5500DC controller, the antenna stack's bias-T
switches, and the local/remote safety inputs.

Everything else in the field unit is either off-the-shelf modules (W5500
Ethernet) or wiring, and is not covered here.

For the GPIO pin assignments referenced throughout this document, see
`GPIO_MAP.txt`.

---

## 1. Yaesu G-5500DC External Control jack — pinout

The G-5500DC exposes position feedback and motor control on a DIN-8
jack on the back panel. As wired in this build:

| Pin | Wire colour    | Function                          | Field unit destination          |
|-----|----------------|-----------------------------------|---------------------------------|
| 1   | white/blue     | Elevation pot wiper (analog out)  | ADC input — AY (PE1 / AIN2)     |
| 2   | white/orange   | CW input (contact closure)        | Motor output A3 (PE3) via FET   |
| 3   | brown          | UP input (contact closure)        | Motor output A1 (PF4) via FET   |
| 4   | orange         | CCW input (contact closure)       | Motor output A4 (PE4) via FET   |
| 5   | white/green    | DOWN input (contact closure)      | Motor output A2 (PE0) via FET   |
| 6   | blue           | Azimuth pot wiper (analog out)    | ADC input — AZ (PD3 / AIN4)     |
| 7   | white/brown    | +8–13 V unregulated reference     | **DO NOT USE FOR POWER**        |
| 8   | green          | Ground                            | Field unit ground (single point)|

Wire colours are specific to this build (the cable was made up locally).
Pin numbering and signal functions are from the Yaesu G-5500DC manual.

**Critical:** pin 7 is unregulated and sags significantly under motor
load due to a current-limiting resistor inside the controller. Do not
power the field unit MCU or any peripherals from this rail — it will
brown out during slews and reset the MCU mid-motion. The field unit
must have an independent supply. Pin 7 is left unconnected in this
build.

**Ground reference:** pin 8 is the Yaesu's ground. The field unit ties
this to its own ground at exactly one point on the perfboard (near the
DIN-8 connector). See "Ground topology" at the end of this document.

---

## 2. ADC input — pot wiper voltage divider

### Purpose

The G-5500DC's external-control jack provides azimuth (pin 6) and
elevation (pin 1) position as analog voltages from the controller's
internal pot wipers. The factory full-scale output is approximately
4.5 V across the rotation range. The TM4C123's ADC accepts 0–3.3 V with
hard saturation above that, so the signal needs to be scaled down before
it reaches the ADC.

The GPIO Breakout BoosterPack already provides per-channel op-amp
buffering (TLV2374, unity gain) and ESD protection (PESD5V0L2UU clamps)
on its analog screw terminals. That means the only thing this circuit
needs to do is **scale the signal to fit the breakout's 0–3.3 V input
window** and **clamp fast transients before they reach the breakout's
protection.**

This circuit is built **per channel** (one for azimuth, one for
elevation). Both channels are wired identically; only the DIN-8 source
pin and the destination BoosterPack screw terminal differ.

### Schematic (per channel)

```
   Yaesu DIN-8                                            GPIO Breakout
   (pin 1 = el, pin 6 = az)                              (AY or AZ terminal)

                  +-------+         +-------+
   o-------+------|  10k  |----+----|  22k  |------+
           |      +-------+    |    +-------+      |
           |                   |                   |
          ===                 ---                  |
          TVS                 10n                 GND  --->  o
          5.0V                ---
          bidir.               |
           |                   |
           +-------------------+----------------------------+
                                                            |
                                                           GND
```

Signal path: wiper → 10 kΩ series → midpoint (tap to BoosterPack) →
22 kΩ to ground. TVS diode and filter cap both reference ground; ground
is shared with the breakout / LaunchPad ground (and with Yaesu ground
via DIN-8 pin 8 at the single tie point).

### Bill of materials (per channel)

| Ref  | Value          | Notes                                   |
|------|----------------|------------------------------------------|
| R1   | 10 kΩ, 1%      | Upper divider leg; metal film           |
| R2   | 22 kΩ, 1%      | Lower divider leg; metal film           |
| C1   | 10 nF ceramic  | Low-pass filter; X7R, 50 V              |
| D1   | SMAJ5.0CA      | Bidirectional TVS, 5.0 V working voltage|

### How it works

**The divider** (R1, R2) produces a scaling ratio of 22 / (10 + 22) ≈
0.688. A 4.5 V input becomes ~3.1 V, comfortably inside the breakout's
3.3 V window with margin for component tolerance and source voltage
drift.

**The divider also provides the current limiting** for fault conditions.
In the worst case of a 24 V transient appearing on the wiper line, the
10 kΩ upper resistor limits fault current into the breakout's input
clamps to ~2 mA, well within their rating. No additional series resistor
is required.

**The TVS diode** (D1) clamps fast transients (ESD, switching surges,
nearby RF events) before they reach either the divider or the breakout.
It does what resistors fundamentally can't: it bounds voltage, not just
current. The breakout's own ESD clamps are sized for ESD events; the
TVS in front means they rarely have to handle anything energetic.

**The filter cap** (C1) forms a low-pass filter with the divider's
output impedance (parallel combination of R1 and R2 ≈ 6.9 kΩ). Cutoff
is around 2.3 kHz, far above the pot wiper's mechanical bandwidth
(fractions of a Hz) and far below RF frequencies of interest. Rejects
high-frequency noise picked up on the cable run from the shack.

**The op-amp buffering happens on the breakout**, not here. The
breakout's TLV2374 presents megohm input impedance to the divider's
~7 kΩ source impedance, so the divider sees effectively no load and
its output isn't skewed by the breakout's input.

### Calibration

This circuit is **not precision-trimmed**. Tolerance comes from:

- Resistor tolerance (1% per part, so the divider ratio is good to ~2%)
- The Yaesu's own voltage-adjust pot setting
- ADC reference accuracy

All of this is absorbed by software calibration on the brain side. The
field unit reports raw normalized 0..1 readings (counts / ADC max); the
brain stores a per-axis calibration mapping those readings to degrees,
which it learns by observing the readings at known mechanical positions
(typically the rotator's end stops).

### Alternative: trim the Yaesu instead

The G-5500DC has a voltage-adjust trim on the back of the controller
that can scale the external-jack output directly. If that trim is set
so the maximum-rotation output reads ~3.28 V, the divider becomes
unnecessary — the signal arrives already in range, and the front-end
simplifies to just the TVS and the filter cap.

This is the cleaner approach **if** the G-5500's front-panel meters
are either unaffected by the trim (verify with a multimeter) or are
acceptable to leave inaccurate (the brain-side UI provides the real
readout). On units where the trim affects the meters, leave it at
factory setting and use this divider instead.

---

## 3. Motor contact outputs — opto-isolator + FET pull-down

### Purpose

The G-5500DC's external control jack accepts four contact-closure
inputs:

| DIN-8 pin | Function | Driven by GPIO        |
|-----------|----------|------------------------|
| 2         | CW       | A3 (PE3)              |
| 3         | UP       | A1 (PF4)              |
| 4         | CCW      | A4 (PE4)              |
| 5         | DOWN     | A2 (PE0)              |

Internally these are pull-up inputs that the controller reads as
"commanded direction"; pulling one to ground commands motion in that
direction, exactly as if the front-panel button were pressed.

The field unit needs to drive these four inputs from the TM4C123's
GPIO outputs. Two requirements:

1. **Galvanic isolation** between the MCU and the Yaesu. The Yaesu's
   internal logic shares ground with its 22 VDC motor circuits and its
   mains-powered supply. A fault on the Yaesu side must not have a path
   into the MCU.

2. **Default-off behavior.** During MCU boot, reset, or brownout, all
   four outputs must remain off (rotator not moving). A floating gate
   or a transiently-high GPIO must never command motion.

This circuit is built **per direction** — four identical instances
(CW, CCW, UP, DOWN).

### Topology

```
   MCU GPIO  --[R3]-->  opto LED (anode)
   MCU GND   --------- opto LED (cathode)

   ----- isolation barrier -----

   local 3.3V ---[R5]---+--- FET gate
                        |
   opto collector ------+
   opto emitter ---- Yaesu GND
   FET gate ------[R6]---- FET source (Yaesu GND)
   FET drain ----------- DIN-8 control pin (2/3/4/5)
   FET source ---------- DIN-8 pin 8 (Yaesu GND)

   plus: R4 (100k) from MCU GPIO to MCU GND, as a default-off pull-down
```

(A proper schematic lives in `docs/schematics/` once drawn in KiCad.)

### Bill of materials (per direction)

| Ref  | Value           | Notes                                            |
|------|-----------------|--------------------------------------------------|
| OK1  | PC817 or sim.   | Opto-isolator (any standard 4-pin DIP)           |
| R3   | 330 Ω, 1/4 W    | Opto LED current limit (MCU side, ~6 mA)         |
| R4   | 100 kΩ          | MCU GPIO pull-down (default-off)                 |
| R5   | 10 kΩ           | Opto output pull-up (Yaesu side, to local 3.3 V) |
| R6   | 1 MΩ            | FET gate pull-down (default-off, safety)         |
| Q1   | 2N7000 / BSS138 | N-channel logic-level MOSFET, TO-92 or SOT-23    |

### How it works

**The opto-isolator** (OK1) provides the galvanic isolation. When the
MCU GPIO is high, current flows through R3 and the opto's LED; the
opto's output transistor conducts. When the MCU GPIO is low (or
floating), the LED is dark and the output transistor is open.

**R3** sets the opto LED current. With a 3.3 V GPIO and a typical LED
forward voltage of ~1.2 V, (3.3 − 1.2) / 330 ≈ 6.4 mA, comfortable
for a PC817 and well within the TM4C123 GPIO's standard 8 mA drive
capability.

**R4** holds the MCU GPIO at ground during reset, boot, and any other
state where the pin is in input mode (high-impedance). Without this,
the floating GPIO could pick up enough noise to weakly turn on the
opto.

**R5** pulls the opto output high when the LED is dark. When the LED
conducts, the output transistor pulls low. The pull-up references the
field unit's local 3.3 V rail on the Yaesu side (see "Ground topology"
below).

**Q1**, the N-channel MOSFET, switches the Yaesu control pin to Yaesu
ground when commanded. Drain connects to the Yaesu's control input pin
(DIN-8 pin 2, 3, 4, or 5); source connects to Yaesu's ground (DIN-8
pin 8); gate is driven by the opto's output. When the gate is high,
the FET conducts and pulls the control input to ground (commanding
motion). When the gate is low, the FET is off and the control input
is left alone (Yaesu sees its own internal pull-up, interprets as "no
command").

**R6** is the critical safety component. Without it, a floating gate
(during the brief window before R5's pull-up settles, or if the opto
fails open) could float up and partially turn on the FET, intermittently
commanding motion. R6 ties the gate firmly to source. 1 MΩ is high
enough not to fight R5's pull-up under normal operation but low enough
to bleed off any gate charge within microseconds.

### Why a FET, not just the opto output directly?

A typical opto-isolator's output transistor can sink only a few mA and
has a saturation voltage of several hundred mV — fine for logic
signaling between two MCU domains, but not ideal for cleanly pulling a
controller input to true ground. The FET adds:

- A near-zero on-resistance (a 2N7000 is ~5 Ω, BSS138 is ~3 Ω) for a
  clean low-side switch
- Higher current capacity if the Yaesu's input happens to source more
  than expected
- Better isolation between the opto's small signal current and the
  Yaesu side's actual control current

For just a logic-level signal, a bare opto-output would work. For
"pretend to be a contact closure to ground," the FET makes it a real
pull-down.

### Why default-off matters here

Of all the field unit's outputs, the four motor-direction lines are
the ones where "accidentally on" has physical consequences: the
antenna starts moving, potentially toward a hard stop or a position
that fouls on something. The MCU's boot sequence, any watchdog-
triggered reset, and any brownout-and-recovery cycle all pass through
states where GPIO configuration is uncertain. R4 and R6 together
guarantee that **the only way for a motor line to be energized is for
the MCU to have explicitly, deliberately driven its GPIO high and held
it there.** Any other state — floating, in reset, mid-boot, brownout —
leaves the FET firmly off.

---

## 4. RF / antenna switch outputs — bias-T drivers

### Purpose

Four GPIO outputs drive DC voltages up coax to bias-T switches at the
antennas. The bias-T's job is to energize a small relay at the antenna
that selects between two states (polarization sense, LNA bypass, RX/TX
path, etc.).

| GPIO output | BoosterPack | Function           | Drives                       |
|-------------|-------------|--------------------|------------------------------|
| PF0         | B1          | VHF polarization   | Bias-T → VHF antenna relay   |
| PC4         | B2          | UHF polarization   | Bias-T → UHF antenna relay   |
| PC5         | B3          | UHF LNA            | Bias-T → UHF LNA bypass/in   |
| PC6         | B4          | UHF RX/TX switch   | Bias-T → UHF TX/RX relay     |

### Topology

The circuit is **electrically identical** to the motor-direction
outputs in Section 3: opto-isolator + N-channel FET pull-down. The
difference is what the FET's drain connects to:

- **Motor outputs:** FET drain to a control pin on the Yaesu DIN-8;
  FET source to Yaesu ground.
- **Bias-T outputs:** FET drain to the bias-T's control input (the DC
  injection point); FET source to field-unit ground.

In other words: the FET acts as a low-side switch that either grounds
the bias-T control input (relay energized — "drive on") or leaves it
open (relay de-energized — "drive off"). Whether "drive on" means
RHCP or LHCP, or "LNA in" or "LNA bypass", is determined entirely by
how the antenna and bias-T are wired and is a brain-side configuration
concern. The field unit reports and accepts only "drive on / drive
off" per output.

### Bill of materials (per output)

Same as Section 3: opto-isolator, R3 (330 Ω), R4 (100 kΩ), R5 (10 kΩ),
R6 (1 MΩ), N-channel FET.

If a particular bias-T draws more than a 2N7000 / BSS138 can comfortably
switch (a few hundred mA), substitute a higher-current logic-level FET
such as IRLML2502 or AO3400 — same package family, same pinout, same
gate drive, higher continuous current rating. Most ham-radio bias-T
relays are well within the small-FET range.

### Default-off behavior

Same as the motor outputs: R4 and R6 ensure that on MCU boot, reset,
or brownout, all four bias-T outputs are off. The bias-T relays
de-energize to their "drive off" state, which the antenna wiring
defines as one of the two polarization senses (the "default" state
of the harness).

This is the rationale for keeping the brain in charge of "what does
drive-off mean": if you ever rewire a harness, the field unit's
behavior is unchanged — only the brain's config (and the operator's
mental model) needs updating.

---

## 5. Hardware safety inputs

### Purpose

Two inputs on the field unit provide hardware-level safety override
independent of the network link:

| GPIO input | BoosterPack | Function                                    | Active |
|------------|-------------|---------------------------------------------|--------|
| PB1        | A9          | Emergency stop (latching)                   | LOW    |
| PB0        | A8          | Park trigger (short press) /                | LOW    |
|            |             | E-stop latch clear (3-second hold)          |        |

### E-stop design — two independent paths

The system has **two independent E-stop paths** with deliberately
different semantics:

#### Hardware E-stop (PB1 / A9)

- Triggered by pulling PB1 low — physical button, external contact,
  or remote switch.
- **Latching.** Sets an `estop_hw_latch` flag in the state machine;
  all motion commands (`SET_MOTION`) are blocked until the latch is
  explicitly cleared.
- Cleared in one of two ways:
  1. **Hardware-only:** hold PB0 (park button) for 3 seconds while PB1
     is released (i.e., the E-stop condition is gone). This allows the
     operator to recover even if the brain is offline.
  2. **Via brain:** the brain sends a `CLEAR_FAULT` command (e.g. from
     a "Clear Fault" UI button or the `rotor fault` CLI).

The latching behavior is deliberate: a hardware E-stop represents a
positive operator action ("stop, and *do not* move again until I
acknowledge"). It must not clear itself just because the trigger
condition went away or a network command rolled past.

#### Software E-stop (brain command)

- Sent via `POST /api/v1/emergency_stop` (or the equivalent CLI / UI
  control).
- **Non-latching.** Sets an `estop_active` flag in the state machine;
  cleared automatically by the next `SET_MOTION` command.
- Semantically equivalent to "abort current motion, ready to accept
  new commands" — a "stop now, I'll send a new target shortly"
  operation, not a permanent shutdown.

This asymmetry is intentional: physical buttons mean "stop and stay
stopped"; software commands mean "stop and stand by." Conflating them
would make either the hardware path unsafe (cleared too easily) or the
software path inconvenient (requires explicit clear after every stop).

**Telemetry and display:** both flags surface in telemetry as
`state=FAULT_*` with `fault_detail="estop"`. The web UI shows
`[E-STOP!]` in both AZ and EL rows when either flag is active.

#### Park button dual function (PB0)

PB0 has two distinct functions distinguished by press duration:

- **Short press (falling edge):** the state machine initiates a move
  to the configured park position (az 180° / el 45°).
- **3-second hold:** clears the hardware E-stop latch, provided PB1
  is currently released (i.e., the E-stop condition is no longer
  active). Holding when PB1 is still low does nothing — you can't
  override an active E-stop.

The 3-second hold provides a deliberate, non-accidental gesture for
clearing the latch. Software debouncing in firmware distinguishes the
two functions; nothing in the hardware needs to know about it.

#### Design note: clearing software E-stop from multiple sources

The current design says software E-stop clears on the next
`SET_MOTION` command. The field unit's command interface is
deliberately source-agnostic (the brain is the only source in v1, but
a future GS-232 UART or local jog could add others). A decision worth
making explicitly: should software E-stop clear on a `SET_MOTION` from
*any* source, or only from the source that set it?

Safer default: **only the originating source can clear its own
software E-stop.** Otherwise a local jog button could accidentally
undo a brain-initiated stop. The brain-issued stop persists until the
brain (or a hardware E-stop or a manual hardware latch clear) ends
it. This adds one field to the state machine's E-stop tracking
(source ID alongside the flag) and is essentially free to implement
now versus painful to retrofit later.

Both inputs have **internal weak pull-ups enabled** in the MCU's GPIO
configuration; the external switch / signal pulls them low. This means:

- A disconnected input reads as HIGH (no command active) — safe default.
- A grounded input reads as LOW (command active).
- A broken wire reads as HIGH — the system reverts to "no command,"
  not "permanent fault." For a *true* fail-safe emergency-stop wiring,
  see "Fail-safe option" below.

### Two installation modes

These inputs can come from either a panel switch on the field unit
enclosure (short wire run, low noise) or a remote panel at the tower
(long wire run, RF-rich environment, potentially long enough for
significant induced noise). The circuit differs.

#### Mode A — panel switch on the field unit enclosure

```
   +3.3V (MCU internal weak pull-up, ~50 kΩ)
            |
            +-------- PB0 or PB1
            |
            /
            \  switch (momentary or latching)
            /
            |
           GND (field unit)
```

Bill of materials: one switch per input. That's it.

Notes:
- Use a momentary normally-open switch for the park trigger (PB0),
  so the falling edge fires once on press.
- Use a latching switch (toggle, push-on/push-off) for emergency stop
  (PB1), so the stop persists until manually cleared.
- Software debouncing of 20–50 ms is sufficient at the firmware
  level; no hardware RC filter needed for short wire runs.

#### Mode B — remote switch on the tower

For a switch at the bottom of the tower or anywhere with a wire run of
more than a few meters, the input needs extra protection because the
wire becomes an antenna for the same RF the rest of the design is
trying to coexist with.

```
   remote switch                       field unit
                                       +3.3V (internal pull-up)
                                          |
   o   o-----shielded--------------+------+--------- PB0 / PB1
   GND |     twisted pair          |      |
       |     (shield grounded     ---    ===
       |      at field unit       100n   TVS 5.0V
       |      end ONLY)           ---    bidir
       |                           |      |
       +---------------------------+------+--------- GND (field unit)
```

Bill of materials (per remote input):
- 1 × bidirectional TVS, SMAJ5.0CA, between input and ground
- 1 × ceramic cap, 100 nF, X7R 50 V, between input and ground
- Shielded twisted-pair cable for the run (one twisted pair carries
  switch signal + return; shield grounded at field unit end only)
- Optional: a small ferrite bead on the wire as it enters the
  enclosure, for extra HF rejection

Notes on the wiring:
- The remote switch's "GND" is the return wire in the twisted pair,
  *not* a separate earth ground at the tower. Both wires run back to
  the field unit; the field unit defines the ground reference.
- The shield is grounded at the field unit only, not at the tower.
  Grounding at both ends creates a ground loop along the cable
  length, which is a worse noise source than no shield at all.
- Software debouncing should be longer here — 50–100 ms — because
  long wire runs can produce contact-bounce-like behavior from
  capacitive coupling even with a clean switch.

#### Fail-safe option (for emergency stop only)

The default wiring (switch closes to ground = stop) means a *broken
wire* reads as "no stop" — i.e., the safety input fails *unsafe*. If
you want a true fail-safe (broken wire = stop), invert the wiring:

- Use a normally-closed switch
- Wire it so that closed = pin grounded = no stop
- Opening the switch (or any wire break) lets the pull-up float the
  pin high, which the firmware interprets as stop

This requires inverting the active level in firmware (an
`#define ESTOP_ACTIVE_HIGH` flag, or similar). For Mode B installation,
this is the recommended wiring — a tower-mounted emergency stop should
fail to "stop," not to "no command."

---

## 6. Ground topology

The field unit has effectively three ground domains that meet at
controlled points:

1. **MCU / field unit ground** — the LaunchPad, BoosterPack, W5500
   module, opto LED sides, and the field unit's own power supply
   return. This is the field unit's reference ground.

2. **Yaesu ground** — DIN-8 pin 8, which is the Yaesu controller's
   internal ground (shared with its motor and mains circuitry).

3. **Antenna / bias-T ground** — the coax shields and bias-T returns
   running up the tower.

For v1 (perfboard build), these are tied together at a single point
near the DIN-8 connector. This is the practical compromise discussed
earlier: full galvanic isolation across the opto-isolators would
require an isolated 3.3 V supply on the Yaesu side, which is more
hardware than the v1 build justifies.

**Rules:**

- The three ground domains tie at **one and only one point** on the
  perfboard, near the DIN-8 connector. Identify this point in the
  layout; do not casually run multiple ground wires between domains.
- W5500 ground is part of domain 1 (MCU ground), not its own domain.
  The Ethernet cable shield should *not* be bonded to chassis at the
  field unit; the W5500 module handles signal-side grounding.
- Any future PCB revision should add an isolated DC-DC converter to
  provide a separate 3.3 V on the Yaesu side, restoring true isolation.

---

## 7. Notes on NMI-locked pins (PF0 and PD7)

Two pins used by this design — PF0 (B1, VHF polarization) and PD7
(B7, W5500 /RST) — are NMI-locked on the TM4C123 by default. The
firmware must unlock the GPIO commit register (GPIOLOCK + GPIOCR) for
ports F and D during initialization before these pins can be
configured as GPIO outputs.

This is a software concern, but it has a hardware implication: during
the window between MCU boot and firmware unlock + configuration, these
pins are in their reset state (input, high-impedance with no internal
pull). For PD7 (W5500 /RST, active low), this means the W5500 could
potentially be held in reset by a noisy floating line until the
firmware initializes the pin and drives it high.

**Recommended:** add a 10 kΩ pull-up resistor from PD7 to 3.3 V on the
perfboard, so the W5500 boots out of reset by default. The firmware
can still pulse /RST low to perform a deliberate reset; the pull-up
just ensures a clean default state during the MCU's boot window. For
PF0 (VHF polarization), the FET gate pull-down (R6) already enforces
default-off, so no additional perfboard pull is needed.

---

## 8. Layout notes for the perfboard

A few practical notes for laying these out on perfboard:

- Keep the **analog input chain straight-line** from the DIN-8
  connector to the breakout's screw terminal. Don't loop back; don't
  run digital signals near the divider midpoint.
- Place the **TVS diodes physically close** to whatever they're
  protecting — at the DIN-8 connector for the ADC inputs, at the
  remote-input terminal block for the safety inputs. They should be
  the first thing a transient sees, not the last.
- Keep the **Yaesu-side ground** of the opto/FET circuits physically
  organized so the single ground-tie point is clearly identifiable.
- The four motor outputs and four bias-T outputs are identical
  circuits — eight instances total. Lay them out as two repeated rows
  of four. Same for the two analog input channels.
- Strain-relieve every wire entering or leaving the perfboard.
  Vibration and cable pulls are the main reason perfboard projects
  die.
- Label every screw terminal on the perfboard with both the GPIO pin
  name (PE3, PF4, etc.) and the function (CW, UP, etc.). You will
  thank yourself.

---

## 9. Future PCB notes

When this moves to a PCB (v2), the changes from this perfboard layout
are mostly:

- Use surface-mount equivalents (smaller TVS, smaller MOSFETs in
  SOT-23, array opto-isolators that combine multiple channels in one
  package such as the TLP291-4)
- Add a proper isolated DC-DC converter for the Yaesu-side 3.3 V rail,
  restoring true galvanic isolation across the opto-isolators
- Add input-protection components (small ferrite beads, BAT54S
  Schottky clamps at the ADC pins) that are belt-and-braces on
  perfboard but cheap on a PCB
- Place the DIN-8 connector, the RJ45, and the remote-input terminal
  block on the board edge to mate with panel cutouts
- Consider an integrated EMI-filter-plus-ESD array (TPD4S012 or
  similar) for the safety-input lines, especially for Mode B

The basic circuits documented here transfer to the PCB unchanged.
