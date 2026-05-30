# Rotator Controller Field Unit — Project Brief

## What this project is

A modern, IP-connected antenna rotator controller. The system has three tiers:

1. **Field unit** — a TI TM4C123 LaunchPad with a W5500 Ethernet module, sitting electrically close to a Yaesu G-5500 azimuth/elevation rotator controller. It reads two pot wipers (az/el) via the G-5500's External Control DIN-8, drives four contact closures (CW/CCW/UP/DOWN) back into the same connector via opto-isolators, and drives one polarization relay. It enforces safety locally and talks IP to the brain. **This brief is about the field unit only.**

2. **Brain** — a Linux daemon that owns the link to the field unit, runs slew/tracking logic, and exposes a REST + WebSocket API. Out of scope for this brief.

3. **Clients** — CLI, web UI, optional Hamlib/GS-232 compatibility shim. Out of scope.

The field unit must be **autonomous and safe on its own**. If the network link drops, motors stop. If a soft limit is hit, motors stop. If duty-cycle limits are exceeded, motors stop. The brain *requests*; the field unit *protects*.

## Hardware

- **MCU board:** EK-TM4C123GXL (TM4C123GH6PM, Cortex-M4F @ 80MHz, 256KB flash, 32KB SRAM)
- **Network:** WIZnet W5500 Ethernet module over SPI (handles TCP/IP in silicon)
- **Sensor inputs:** 2 analog channels (az pot wiper, el pot wiper) from G-5500 DIN-8.
  Pot wipers must be conditioned to ≤3.28V before reaching the MCU ADC.
  Assume a simple op-amp buffer + divider on the input side (external to firmware concerns).
- **Motor outputs:** 4 GPIOs → opto-isolators → contact closures back into G-5500's external control jack
  (CW, CCW, UP, DOWN). The G-5500 does the actual 22VDC motor drive.
- **Polarization output:** 1 GPIO → solid-state relay → bias-T control voltage (boolean: drive on/off).
- **Power:** independent supply for the field unit. **Do not power from the G-5500's pin-7 rail** — it sags
  significantly under motor load and will brown out the MCU.
- **Debug:** on-board ICDI debugger over USB (CMSIS-DAP, works with OpenOCD).

## Toolchain (non-negotiable)

- VS Code as editor
- `arm-none-eabi-gcc` toolchain
- CMake as the build system (not Make, not PlatformIO, not Code Composer Studio)
- OpenOCD for flashing and debugging
- TivaWare vendored into the repo under `lib/tivaware/` as plain source — no package manager pulling it in
- No RTOS. Single main loop, fixed tick, explicit state machine. See "Firmware architecture" below.

The project must build with a single `cmake --build build` after a one-time `cmake -B build`.
No proprietary tools, no license servers, no IDE-specific project files checked in.

## Firmware architecture

### Top-level shape

A single fixed-tick main loop (10ms target tick, so 100Hz). Per tick:

1. Read the latest filtered ADC samples (DMA-driven in the background, hardware ADC sequencer).
2. Convert raw ADC counts to a normalized 0.0–1.0 position for each axis.
   **The field unit does NOT convert to degrees.** Degrees are a brain-tier concept that depends on
   calibration; the field unit reports the honest voltage / normalized reading only.
3. Check the command socket for any new command from the brain. Parse and validate.
4. Update the safety state machine (see below).
5. Update motor and polarization GPIOs based on the state machine output.
6. Send a telemetry frame (current az/el reading, polarization drive state, fault state) to the brain.
7. Kick the hardware watchdog.

The state machine has these states at minimum:
- `IDLE` — no motion commanded
- `MOVING_CW`, `MOVING_CCW`, `MOVING_UP`, `MOVING_DOWN` — one axis moving in one direction
  (azimuth and elevation are independent; both can be active simultaneously)
- `FAULT_LINK_LOST` — no command/heartbeat from brain within timeout
- `FAULT_LIMIT` — a soft limit was reached
- `FAULT_DUTY_CYCLE` — cumulative motor-on time exceeded the 3-min-on / 15-min-rest envelope
- `FAULT_ADC_INVALID` — ADC readings failed plausibility checks (e.g. rate-of-change too high, suggesting RF noise spike)

Faults latch until cleared by an explicit command from the brain.

### Command source abstraction

Commands enter the state machine via a single internal interface. v1 has exactly one command source —
the TCP socket. **Design the command-input path so that adding a second source (e.g. a UART GS-232 emulator)
later is purely additive.** Each command carries a source identifier and a priority. Highest priority wins
when sources conflict. Do not hardcode "the network commands me"; the state machine should accept commands
from an abstract source.

### ADC handling

- Use the TM4C123's ADC0 with the hardware sample sequencer, configured to sample both channels
  on a timer trigger at ~1kHz.
- DMA the samples into a ring buffer in SRAM. Main loop reads from this buffer without blocking.
- Apply a **median filter** over the last N samples (start with N=16) — median, not mean, because median
  rejects RF spikes that would skew an average.
- Apply a **rate-of-change plausibility check**: if the new filtered reading differs from the previous
  by more than physically possible in one tick (the rotator's rated rotation speed implies a max delta),
  reject the sample and increment a noise counter. If the noise counter exceeds a threshold within a window,
  transition to `FAULT_ADC_INVALID`.

### Safety

- **Link-loss watchdog:** if no command or heartbeat received from the brain in 2 seconds, transition to
  `FAULT_LINK_LOST` and stop all motion. Configurable timeout.
- **Hardware watchdog:** the MCU's hardware watchdog timer must be kicked every main-loop iteration.
  If the main loop hangs, the MCU resets, which on boot puts all outputs in the safe state
  (motors off, polarization at default).
- **Soft limits:** the brain may push soft-limit values to the field unit at startup. The field unit
  enforces them locally. Defaults are conservative (e.g. az 0–450 in normalized units mapped from raw,
  el 0–180 mapped from raw — actual values TBD; expose them as configurable).
- **Duty-cycle tracking:** track cumulative motor-on time per axis. Enforce 3 min on / 15 min rest per
  the G-5500 manual.
- **Power-on safe state:** on boot, all outputs are de-energized. The unit comes up in `IDLE` and waits
  for the brain to connect before accepting any motion commands.

## Wire protocol (field unit ↔ brain)

This is the seam — get it right. v1 protocol:

- Two sockets on the W5500: one **TCP** for commands and acks (port TBD, default 7700),
  one **UDP** for telemetry blasts to the brain (port TBD, default 7701).
- All messages are **newline-delimited JSON**. Keep it human-debuggable with `nc`.
- Every message has a `type` field and a `seq` field (monotonic uint32 per direction).

### Commands (brain → field unit, TCP)

```
{"type":"hello", "seq":N, "client":"brain-v1"}
{"type":"set_motion", "seq":N, "az":"cw"|"ccw"|"stop", "el":"up"|"down"|"stop"}
{"type":"set_polarization", "seq":N, "drive":true|false}
{"type":"set_limits", "seq":N, "az_min":x, "az_max":x, "el_min":x, "el_max":x}  // normalized 0..1
{"type":"clear_fault", "seq":N}
{"type":"heartbeat", "seq":N}
{"type":"emergency_stop", "seq":N}
```

### Acks (field unit → brain, TCP, in response to commands)

```
{"type":"ack", "seq":N, "ok":true|false, "error":"..."}
```

### Telemetry (field unit → brain, UDP, ~20Hz)

```
{
  "type":"telemetry",
  "seq":N,
  "ts_ms":<unsigned ms since boot>,
  "az_raw":<0..1 float>,
  "el_raw":<0..1 float>,
  "az_motion":"cw"|"ccw"|"stop",
  "el_motion":"up"|"down"|"stop",
  "polarization":true|false,
  "state":"IDLE"|"MOVING"|"FAULT_LINK_LOST"|"FAULT_LIMIT"|...,
  "fault_detail":"...",
  "duty_az_pct":<0..100>,
  "duty_el_pct":<0..100>
}
```

Telemetry is lossy by design (UDP). Commands are reliable (TCP). Do not invert this.

## Repo layout

```
/CMakeLists.txt
/cmake/                  # toolchain file for arm-none-eabi
/src/
  main.c                 # main loop, tick scheduler
  state_machine.{c,h}    # the state machine
  adc.{c,h}              # ADC + DMA setup, median filter, plausibility
  command.{c,h}          # command source abstraction, JSON parsing
  net.{c,h}              # W5500 driver glue, TCP + UDP socket handling
  safety.{c,h}           # watchdog, limits, duty-cycle tracking
  gpio_outputs.{c,h}     # motor + polarization output abstraction
  protocol.{c,h}         # wire protocol encode/decode
/lib/
  tivaware/              # vendored TI TivaWare (peripheral driver lib)
  w5500/                 # vendored W5500 driver (ioLibrary_Driver or similar)
/openocd/
  tm4c123.cfg            # OpenOCD config for the on-board ICDI
/docs/
  BRIEF.md               # this file
  PROTOCOL.md            # the wire protocol spec, expanded
  SAFETY.md              # safety invariants and how they are enforced
/test/
  host_tests/            # whatever can be unit-tested on the host (parser, state machine)
```

## What to build first, in order

1. **Skeleton + blink.** CMake project, toolchain file, OpenOCD config, TivaWare vendored, blink the
   on-board LED. Verify flash and debug work end-to-end via the ICDI.
2. **Tick scheduler.** A 100Hz main loop driven by a hardware timer. Toggle an LED at 1Hz from the
   tick to prove timing.
3. **ADC pipeline.** Both channels, hardware sequencer, DMA into ring buffer, median filter,
   plausibility check, exposed as `adc_get_az()` / `adc_get_el()` returning normalized floats.
4. **GPIO outputs.** Abstract motor and polarization outputs. Stub the state machine to drive them
   on a timer (e.g. cycle through states every 5 seconds) to verify wiring without a brain.
5. **State machine.** All states, all transitions, fault latching. Unit-testable on the host — write
   host tests for it.
6. **W5500 driver integration.** Bring up Ethernet, get a static IP, open TCP and UDP sockets.
   Echo test first (mirror bytes back) before any protocol.
7. **Wire protocol.** JSON encode/decode, command parsing, ack generation, telemetry emission.
   Host-testable; write tests.
8. **Safety integration.** Hardware watchdog kicked from main loop, link-loss timeout, duty-cycle
   tracker. Verify by unplugging Ethernet mid-motion and confirming motors stop.

Each step should result in a working, flashed firmware that does something visible. Do not skip ahead.

## Hard rules

- **No dynamic allocation after boot.** No `malloc` in the steady state. All buffers statically sized.
- **No floating point in interrupt handlers.** FPU is fine in the main loop; not in ISRs.
- **All ISRs are short.** Set a flag, push to a ring buffer, return. No parsing, no logic.
- **No `printf` in production paths.** A small `debug_log()` over UART0 (ICDI virtual COM) is fine
  for development, gated behind a compile-time flag.
- **All magic numbers named.** Timeouts, thresholds, pin assignments — all `#define`d or `const`,
  never inline literals.
- **The state machine is the only thing that writes to motor GPIOs.** Not the command parser,
  not the network layer. One writer.
- **Comments explain *why*, not *what*.** The code already says what it does.

## Out of scope (do not build)

- Any degree-based math (that's brain-tier)
- Any tracking logic (satellite, Moon, sun) — brain-tier
- Any UI, CLI, or web interface — brain-tier
- GS-232 or Hamlib emulation — deferred, even though the architecture supports adding it later
- Multi-rotator support — single rotator, single brain connection
- Persistent configuration / EEPROM — config comes from the brain on connect
- OTA firmware updates — flash via OpenOCD for now

## Questions to ask before assuming

If anything in this brief is ambiguous, **ask before guessing**. Specifically:
- Exact pin assignments for ADC inputs, GPIO outputs, and SPI to the W5500 — I will provide these
  when you get to the relevant step.
- Whether the W5500 driver of choice is WIZnet's official `ioLibrary_Driver` or a slimmer alternative.
- Soft limit values — these are calibration-dependent and will be set by the brain at runtime; use
  permissive defaults (full 0..1 range) for now.
