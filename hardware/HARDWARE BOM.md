# Bill of Materials

This BOM covers a complete v1 field unit — the LaunchPad, the breakout
BoosterPack, the Ethernet module, an external 5V supply, and all the
per-channel passive and active components for the two ADC input
channels, the four motor-direction outputs, the four bias-T outputs,
and the two safety inputs.

Prices and stock vary by supplier; this BOM lists representative parts
and indicative quantities rather than line-item costs.

---

## 1. Main modules

| Qty | Part                            | Description                                                                 | Supplier / notes                          |
|----:|---------------------------------|-----------------------------------------------------------------------------|--------------------------------------------|
| 1   | TI EK-TM4C123GXL                | Tiva C LaunchPad — TM4C123GH6PM (Cortex-M4F @ 80 MHz, 256 KB flash)        | TI store, Mouser, Digi-Key, Farnell        |
| 1   | TI BOOSTXL-IOBKOUT              | GPIO Breakout BoosterPack — screw terminals, ESD protection, op-amp buffers| TI store, Mouser, Digi-Key                 |
| 1   | WIZnet WIZ550io                 | Ethernet module — W5500 + transformer + RJ45 + factory MAC + auto-init    | WIZnet shop, Mouser, Digi-Key — see notes |
| 1   | 5 V / 1 A DC supply             | External wall wart, 5.5 × 2.1 mm barrel plug typical                       | Any quality brand; feeds VBUS via barrel jack adapter |

### Power-feed notes

The 5 V supply feeds the LaunchPad via its VBUS pin (J3.1 on the
BoosterPack header) with SW3 set to "Device" or via a 5 V barrel jack
wired to the same VBUS net on the perfboard. ICDI USB can remain
connected for debug; current ICDI USB hosts current-limit cleanly so
back-feed conflicts have not caused issues in practice.

The 5 V supply also feeds:
- The WIZ550io's 5 V input (the module has an on-board 3.3 V regulator)
- Any future field-unit-side circuitry that needs 5 V

The LaunchPad's on-board LDO derives 3.3 V from this 5 V rail and
supplies the BoosterPack and the rest of the 3.3 V loads. A 1 A
external supply is comfortable headroom for the field unit's total
budget (~300 mA peak during W5500 link negotiation plus everything
else).

### WIZ550io notes — buy the genuine module, not a clone

Generic W5500 modules from marketplaces (Amazon, AliExpress) are
nominally compatible but vary widely in PCB layout, decoupling, and
EMI behavior. The genuine WIZ550io has engineered layout, proper
impedance control, and integrated transformer + RJ45 — pay the
premium. Cheap clones in this build produced unstable SPI behavior
that took significant time to diagnose; the genuine module worked
reliably out of the box.

### SPI clock speed — keep it conservative on perfboard

The W5500 is rated for SPI clocks up to ~80 MHz on a properly designed
PCB. **On perfboard with jumper wires between a LaunchPad and a
module, this is wildly optimistic.** Parasitic inductance, lack of a
ground plane, and uncontrolled trace impedance turn high-frequency
SPI edges into noisy, ringing signals that the W5500 samples
incorrectly. The result is silently corrupted register reads/writes
that look like firmware bugs but are actually signal-integrity
failures.

**Recommended:** start at **4 MHz** SPI clock. Move higher only if
you have a specific throughput requirement (you don't — telemetry at
20 Hz needs maybe 5 KB/s of bus traffic, which 1 MHz can deliver with
25× headroom). Drop further if anything misbehaves.

This experience is documented because it cost real debugging time
before being identified. The lesson generalizes: on perfboard, treat
maximum-rated speeds as theoretical and start conservative.

---

## 2. ADC input front-end (2 channels)

One set of components per channel; the BOM below totals across both
azimuth and elevation channels.

| Qty | Part                | Value          | Description                                |
|----:|---------------------|----------------|--------------------------------------------|
| 2   | Resistor            | 10 kΩ, 1 %     | Divider upper leg, metal film, 1/4 W       |
| 2   | Resistor            | 22 kΩ, 1 %     | Divider lower leg, metal film, 1/4 W       |
| 2   | Ceramic capacitor   | 10 nF          | Low-pass filter, X7R, 50 V                 |
| 2   | TVS diode           | SMAJ5.0CA      | Bidirectional, 5 V working voltage         |

Reference designators in HARDWARE.md: R1, R2, C1, D1 (per channel).

---

## 3. Motor contact outputs (4 channels)

Four identical instances driving CW / CCW / UP / DOWN on the Yaesu
DIN-8. Each instance is one opto-isolator + one N-channel MOSFET
+ four resistors.

| Qty | Part                | Value          | Description                                |
|----:|---------------------|----------------|--------------------------------------------|
| 4   | Opto-isolator       | PC817          | 4-pin DIP, single channel                  |
| 4   | N-channel MOSFET    | 2N7000         | TO-92, logic-level (BSS138 SOT-23 also OK) |
| 4   | Resistor            | 330 Ω, 1/4 W   | R3 — opto LED current limit (~6 mA)        |
| 4   | Resistor            | 100 kΩ         | R4 — MCU GPIO pull-down                    |
| 4   | Resistor            | 10 kΩ          | R5 — opto output pull-up to 3.3 V          |
| 4   | Resistor            | 1 MΩ           | R6 — FET gate pull-down (safety)           |

Reference designators in HARDWARE.md: OK1, Q1, R3, R4, R5, R6.

---

## 4. Bias-T outputs (4 channels)

Four identical instances driving the bias-T relays for VHF pol,
UHF pol, UHF LNA, and UHF RX/TX. **The circuit is identical to the
motor outputs** — same part list, same reference designators.

| Qty | Part                | Value          | Description                                |
|----:|---------------------|----------------|--------------------------------------------|
| 4   | Opto-isolator       | PC817          | 4-pin DIP                                  |
| 4   | N-channel MOSFET    | 2N7000         | TO-92 (substitute IRLML2502 / AO3400 if a particular bias-T draws > 200 mA) |
| 4   | Resistor            | 330 Ω, 1/4 W   |                                            |
| 4   | Resistor            | 100 kΩ         |                                            |
| 4   | Resistor            | 10 kΩ          |                                            |
| 4   | Resistor            | 1 MΩ           |                                            |

If any of your bias-Ts draw more than the 2N7000 will switch
comfortably, swap to a higher-current logic-level FET in the same
pinout family (TO-92: BS170 is a drop-in for slightly higher current;
SOT-23: AO3400 or IRLML2502 if you're soldering surface-mount).

---

## 5. Hardware safety inputs (2 inputs)

The safety inputs (emergency stop, park) need different components
depending on whether each switch lives on the field unit's enclosure
(Mode A) or is remoted to the tower (Mode B). The BOM below covers
both modes; pick the row that matches each input's intended
installation.

### Mode A — switches on field unit enclosure

| Qty | Part                       | Description                                          |
|----:|----------------------------|------------------------------------------------------|
| 1   | Panel-mount latching switch| For Emergency Stop (PB1) — push-on/push-off type, ideally with a guard or mushroom head |
| 1   | Panel-mount momentary switch| For Park (PB0) — normally-open, returns to released |

### Mode B — remote switches at the tower

Add per remote input:

| Qty | Part                | Value          | Description                                |
|----:|---------------------|----------------|--------------------------------------------|
| 1   | TVS diode           | SMAJ5.0CA      | Bidirectional, at field unit input         |
| 1   | Ceramic capacitor   | 100 nF         | X7R 50 V, at field unit input              |
| 1   | Ferrite bead        | ~100 Ω @ 100 MHz | Optional, through-hole, at enclosure entry|
| —   | Shielded twisted-pair cable | per run length | Single twisted pair + braid shield, e.g. CAT5 with shield, or proper instrumentation cable |

Plus the actual switches at the tower end (specs same as Mode A
counterparts).

For an emergency stop wired in **fail-safe** mode (broken wire = stop),
use a normally-closed switch instead of normally-open. Firmware-side
flag (`ESTOP_ACTIVE_HIGH` or equivalent) handles the inversion.

---

## 6. Boot-state pull-up (W5500 reset line)

Per HARDWARE.md §7, PD7 (W5500 /RST) should have a pull-up to 3.3 V on
the perfboard so the W5500 boots out of reset by default during the
window before firmware unlocks PD7.

| Qty | Part                | Value          | Description                                |
|----:|---------------------|----------------|--------------------------------------------|
| 1   | Resistor            | 10 kΩ          | Pull-up from PD7 to 3.3 V                  |

---

## 7. Connectors, wiring, mechanical

The exact mix depends on your enclosure choice and cable lengths,
but the field unit needs at minimum:

| Qty | Item                | Description                                                |
|----:|---------------------|------------------------------------------------------------|
| 1   | DIN-8 panel jack    | 270° (NOT 180°) layout, matches Yaesu's external jack      |
| 1   | RJ45 panel pass-through | Or a cutout for the WIZ550io's RJ45 to protrude       |
| 1   | DC barrel jack, panel-mount | 5.5 × 2.1 mm to match the 5 V supply               |
| 4   | Coax bias-T injection points | BNC or SO-239 panel-mount, per local convention   |
| 2   | Panel switches (Mode A) or terminal blocks (Mode B) | for safety inputs |
| 1   | Enclosure           | Diecast aluminium recommended (e.g. Hammond 1590-series) for RF environments |
| —   | Hookup wire         | 22 AWG stranded for signal, 18 AWG for power/return        |
| —   | Strain reliefs      | Cable glands for every wire passing through the enclosure  |
| —   | Standoffs / mounting hardware | M3 typical for LaunchPad and perfboard           |
| 1   | Perfboard           | ~100 × 80 mm; enough room for ~10 opto/FET cells + 2 ADC channels |
| 1   | 0.1" header pin strip | For LaunchPad-to-perfboard signal connections           |

---

## 8. Tools / consumables

Listed for completeness; not part of the build itself but needed to
assemble it.

- Soldering iron with a fine tip
- 0.6 mm or 0.8 mm rosin-core solder
- Flux pen (helpful for the surface-mount FETs if you go SOT-23)
- Multimeter — at minimum continuity, voltage, and resistance modes
- Wire strippers, side cutters, small needle-nose pliers
- Heat-shrink tubing in assorted sizes
- A 5 V bench supply or known-good wall wart for bring-up before the
  final supply is wired in

---

## 9. Summary headcount

For mental arithmetic — what's the total parts count for the active
circuits?

- ADC front-end: 2 channels × 4 components = 8 passives
- Motor outputs: 4 channels × 6 components = 24 (4 ICs, 4 FETs, 16 resistors)
- Bias-T outputs: 4 channels × 6 components = 24 (4 ICs, 4 FETs, 16 resistors)
- Safety inputs (Mode B both): 2 × 2 = 4 protection components
- W5500 pull-up: 1 resistor
- Switches and connectors: see above

Active components: 8 opto-isolators, 8 FETs.
Resistors and small passives: ~60.

Total active-circuit BOM cost (excluding modules and enclosure):
estimated $15-25 depending on supplier and whether you buy parts
individually or as kits.

---

## 10. Suppliers

For a project of this scale a single mixed order from one distributor
is most economical:

- **Mouser, Digi-Key, Farnell/element14** — every part in this BOM is
  available from any of these in single quantities; shipping is the
  dominant cost so consolidate.
- **TI store** — the LaunchPad and the BoosterPack ship from TI
  directly, sometimes faster than via a distributor; check whichever
  has the part you need in stock.
- **WIZnet shop** for the WIZ550io if regional distributors don't
  stock it; also available via Mouser and Digi-Key.
- **Amazon / AliExpress** — fine for the enclosure, switches, hookup
  wire, and cable glands; avoid for the active components (resistors,
  FETs, opto-isolators) where part-tolerance and genuineness matter.
- **Local hamfest / fleamarket** — DIN-8 panel jacks specifically can
  be hard to find new at sensible prices; the ham-radio second-hand
  market often has them cheaply.
